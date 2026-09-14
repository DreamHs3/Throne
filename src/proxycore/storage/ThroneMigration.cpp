#include "include/proxycore/storage/ThroneMigration.h"

#include <QCryptographicHash>
#include <QDir>
#include <QDirIterator>
#include <QFile>
#include <QFileInfo>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QStandardPaths>
#include <utility>

#include <3rdparty/SQLiteCpp/include/SQLiteCpp.h>
// NOTE: the vendored umbrella header does not pull in Backup.h (unlike
// upstream SQLiteCpp), so include it directly. Backup.cpp is compiled
// into the target, only the declaration was missing.
#include <3rdparty/SQLiteCpp/include/Backup.h>

namespace ProxyCore::Storage {

    namespace {
        constexpr auto MANIFEST_NAME = "migration-manifest.json";
        constexpr auto STAGING_NAME = "migration-staging";
        constexpr auto DB_NAME = "throne.db";

        QString sha256OfFile(const QString& path) {
            QFile f(path);
            if (!f.open(QIODevice::ReadOnly)) return {};
            QCryptographicHash hash(QCryptographicHash::Sha256);
            char buf[65536];
            while (true) {
                const auto n = f.read(buf, sizeof(buf));
                if (n <= 0) break;
                hash.addData(buf, int(n));
            }
            return QString::fromLatin1(hash.result().toHex());
        }

        bool snapshotSqliteIntoStaging(const QDir& source, const QDir& staging,
                                       const QString& rel, QString* error);

        // Copies sourceDir -> stagingDir, returning relative paths of staged
        // files. Never writes inside sourceDir.
        //
        // SQLite databases are not plain-copied: a running Throne keeps them
        // in WAL mode, so a byte copy can be torn and committed state can live
        // in the WAL. Each *.db is read through SQLite's Online Backup API and produces a
        // consistent Online Backup snapshot — the same WAL-safe pattern as
        // Database::backupSelective (src/database/Database.cpp).
        bool copyIntoStaging(const QDir& source, const QDir& staging, QStringList* relPaths, QString* error) {
            QDirIterator it(source.absolutePath(),
                            QDir::Files | QDir::Hidden | QDir::NoDotAndDotDot,
                            QDirIterator::Subdirectories);
            while (it.hasNext()) {
                const QString abs = it.next();
                const QString rel = QDir::fromNativeSeparators(source.relativeFilePath(abs));
                if (rel.startsWith("..")) {
                    *error = QObject::tr("unexpected path outside source: %1").arg(abs);
                    return false;
                }
                if (rel.endsWith(".db-wal", Qt::CaseInsensitive) || rel.endsWith(".db-shm", Qt::CaseInsensitive)) {
                    // Sidecars are consumed by the snapshot of their own
                    // database, never shipped as files of their own.
                    continue;
                }
                const QString dest = staging.absoluteFilePath(rel);
                if (!QDir().mkpath(QFileInfo(dest).absolutePath())) {
                    *error = QObject::tr("cannot create staging directory for %1").arg(rel);
                    return false;
                }
                if (rel.endsWith(".db", Qt::CaseInsensitive)) {
                    if (!snapshotSqliteIntoStaging(source, staging, rel, error)) {
                        return false;
                    }
                } else if (!QFile::copy(abs, dest)) {
                    *error = QObject::tr("cannot copy %1").arg(rel);
                    return false;
                }
                relPaths->append(rel);
            }
            return true;
        }

        // Reads the live source database through SQLite and writes a consistent
        // Online Backup snapshot into staging. SQLite coordinates the snapshot
        // with an active WAL writer; copying the database and sidecars as
        // independent files would have a race between those copies.
        bool snapshotSqliteIntoStaging(const QDir& source, const QDir& staging, const QString& rel, QString* error) {
            bool ok = false;
            try {
                SQLite::Database live(source.absoluteFilePath(rel).toStdString(), SQLite::OPEN_READONLY);
                SQLite::Database snapshot(staging.absoluteFilePath(rel).toStdString(),
                                          SQLite::OPEN_READWRITE | SQLite::OPEN_CREATE);
                SQLite::Backup backup(snapshot, live);
                backup.executeStep(-1);
                ok = true;
            } catch (const std::exception& e) {
                *error = QObject::tr("cannot snapshot %1: %2").arg(rel, QString::fromUtf8(e.what()));
            }
            return ok;
        }
    }

    MigrationResult MigrateFromThrone(const QString& sourceDir, const QString& targetConfigDir) {
        MigrationResult result;

        const QDir source(sourceDir);
        if (!source.exists()) {
            result.error = QObject::tr("source directory does not exist: %1").arg(sourceDir);
            return result;
        }
        if (!QFileInfo::exists(source.absoluteFilePath(DB_NAME))) {
            result.error = QObject::tr("no %1 in %2 — not a Throne data directory").arg(DB_NAME, sourceDir);
            return result;
        }

        const QDir target(targetConfigDir);
        if (!target.exists() && !QDir().mkpath(targetConfigDir)) {
            result.error = QObject::tr("cannot create target directory: %1").arg(targetConfigDir);
            return result;
        }
        if (QFileInfo::exists(target.absoluteFilePath(DB_NAME)) ||
            QFileInfo::exists(target.absoluteFilePath(MANIFEST_NAME))) {
            result.error = QObject::tr("target already initialized, refusing to overwrite: %1").arg(targetConfigDir);
            return result;
        }

        const QString stagingPath = target.absoluteFilePath(STAGING_NAME);
        QDir staging(stagingPath);
        // Only ever contains leftovers of our own failed run, so removing it is safe.
        if (staging.exists() && !staging.removeRecursively()) {
            result.error = QObject::tr("cannot clean stale staging directory: %1").arg(stagingPath);
            return result;
        }
        if (!QDir().mkpath(stagingPath)) {
            result.error = QObject::tr("cannot create staging directory: %1").arg(stagingPath);
            return result;
        }

        QStringList relPaths;
        QString error;
        if (!copyIntoStaging(source, staging, &relPaths, &error)) {
            staging.removeRecursively();
            result.error = error;
            return result;
        }

        // Copy phase complete: move staging contents into their final places.
        // A move failure here rolls back only files this run created.
        QStringList moved;
        for (const QString& rel : std::as_const(relPaths)) {
            const QString src = staging.absoluteFilePath(rel);
            const QString dest = target.absoluteFilePath(rel);
            if (!QDir().mkpath(QFileInfo(dest).absolutePath()) ||
                !QFile::rename(src, dest)) {
                error = QObject::tr("cannot move %1 into place").arg(rel);
                for (const QString& done : std::as_const(moved)) QFile::remove(target.absoluteFilePath(done));
                staging.removeRecursively();
                result.error = error;
                return result;
            }
            moved.append(rel);
        }
        staging.removeRecursively();

        // Manifest is written last: its presence marks a completed migration.
        QJsonArray files;
        qint64 totalBytes = 0;
        for (const QString& rel : std::as_const(relPaths)) {
            const QFileInfo info(target.absoluteFilePath(rel));
            totalBytes += info.size();
            files.append(QJsonObject{
                {"path", rel},
                {"size", double(info.size())},
                {"sha256", sha256OfFile(info.absoluteFilePath())},
            });
        }
        const QJsonObject manifest{
            {"format", 1},
            {"source", QDir::fromNativeSeparators(source.absolutePath())},
            {"target", QDir::fromNativeSeparators(target.absolutePath())},
            {"files", files},
        };
        QFile manifestFile(target.absoluteFilePath(MANIFEST_NAME));
        if (!manifestFile.open(QIODevice::WriteOnly | QIODevice::Truncate) ||
            manifestFile.write(QJsonDocument(manifest).toJson(QJsonDocument::Indented)) < 0) {
            for (const QString& done : std::as_const(moved)) QFile::remove(target.absoluteFilePath(done));
            result.error = QObject::tr("cannot write migration manifest");
            return result;
        }
        manifestFile.close();

        result.ok = true;
        result.filesCopied = relPaths.size();
        result.bytesCopied = totalBytes;
        result.manifestPath = manifestFile.fileName();
        return result;
    }

    RollbackResult RollbackMigration(const QString& targetConfigDir) {
        RollbackResult result;

        QFile manifestFile(QDir(targetConfigDir).absoluteFilePath(MANIFEST_NAME));
        if (!manifestFile.open(QIODevice::ReadOnly)) {
            result.error = QObject::tr("no migration manifest in %1 — nothing to roll back").arg(targetConfigDir);
            return result;
        }
        const QJsonObject manifest = QJsonDocument::fromJson(manifestFile.readAll()).object();
        manifestFile.close();

        const QDir target(targetConfigDir);
        const auto files = manifest["files"].toArray();
        for (const auto& value : files) {
            const QString rel = value.toObject()["path"].toString();
            if (rel.isEmpty() || rel.startsWith("..")) continue;
            // Only files recorded in the manifest are removed; anything the
            // application created after migration stays.
            if (QFile::remove(target.absoluteFilePath(rel))) result.filesRemoved++;
        }
        QDir(target.absoluteFilePath(STAGING_NAME)).removeRecursively();

        if (!manifestFile.remove()) {
            result.error = QObject::tr("cannot remove migration manifest");
            return result;
        }
        result.ok = true;
        return result;
    }

    QString DefaultThroneDataDir() {
        const QString ours = QStandardPaths::writableLocation(QStandardPaths::AppConfigLocation);
        // Throne keeps its data in the "config" subdirectory of its data dir.
        return QDir(QFileInfo(ours).absolutePath()).filePath("Throne/config");
    }

}
