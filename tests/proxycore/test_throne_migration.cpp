// PC-010 migration/coexistence characterization tests. QtCore only, so the
// harness does not pull the QtTest module into the CI toolchain set. Enable
// with -DPROXYCORE_BUILD_TESTS=ON and run via ctest (or the binary directly).

#include <QCoreApplication>
#include <QCryptographicHash>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QStandardPaths>
#include <QTemporaryDir>

#include <SQLiteCpp/SQLiteCpp.h>

#include "include/proxycore/storage/ThroneMigration.h"

#define CHECK(cond)                                                                        \
    do {                                                                                   \
        if (!(cond)) {                                                                     \
            qWarning("CHECK failed at %s:%d: %s", __FILE__, __LINE__, #cond);              \
            failures++;                                                                    \
        }                                                                                  \
    } while (false)

namespace {

    QByteArray hashFile(const QString& path) {
        QFile f(path);
        if (!f.open(QIODevice::ReadOnly)) return {};
        return QCryptographicHash::hash(f.readAll(), QCryptographicHash::Sha256);
    }

    bool writeFile(const QString& path, const QByteArray& content) {
        QDir().mkpath(QFileInfo(path).absolutePath());
        QFile f(path);
        if (!f.open(QIODevice::WriteOnly | QIODevice::Truncate)) return false;
        return f.write(content) == content.size();
    }

    // A small synthetic Throne data directory: a real SQLite database at the
    // root plus nested files.
    void makeThroneSource(const QDir& dir) {
        SQLite::Database db(dir.absoluteFilePath("throne.db").toStdString(),
                            SQLite::OPEN_READWRITE | SQLite::OPEN_CREATE);
        db.exec("CREATE TABLE marker(v TEXT)");
        db.exec("INSERT INTO marker VALUES('PC-TEST')");
        writeFile(dir.absoluteFilePath("config/profiles.json"), R"([{"tag":"example"}])");
        writeFile(dir.absoluteFilePath("config/sub/urls.txt"), "https://example.com/sub\n");
        writeFile(dir.absoluteFilePath(".hidden-marker"), "hidden");
    }

    bool markerRowReadable(const QString& dbPath, const char* expected) {
        try {
            SQLite::Database db(dbPath.toStdString(), SQLite::OPEN_READONLY);
            SQLite::Statement query(db, "SELECT v FROM marker");
            return query.executeStep() && query.getColumn(0).getString() == expected;
        } catch (const std::exception&) {
            return false;
        }
    }

    int testMigrateFreshCopy(const QDir& source) {
        int failures = 0;
        QTemporaryDir target;
        CHECK(target.isValid());

        const auto sourceDbHashBefore = hashFile(source.filePath("throne.db"));
        const auto result = ProxyCore::Storage::MigrateFromThrone(source.absolutePath(), target.path());
        CHECK(result.ok);
        CHECK(result.filesCopied == 4);
        CHECK(QFile::exists(target.filePath("throne.db")));
        CHECK(QFile::exists(target.filePath("config/profiles.json")));
        CHECK(QFile::exists(target.filePath("config/sub/urls.txt")));
        CHECK(QFile::exists(target.filePath(".hidden-marker")));
        CHECK(hashFile(target.filePath("throne.db")) == hashFile(source.filePath("throne.db")));
        CHECK(QDir(target.filePath("migration-staging")).exists() == false);

        // Manifest written last, listing every copied file.
        QFile manifest(target.filePath("migration-manifest.json"));
        CHECK(manifest.open(QIODevice::ReadOnly));
        if (manifest.isOpen()) {
            const auto obj = QJsonDocument::fromJson(manifest.readAll()).object();
            CHECK(obj["files"].toArray().size() == 4);
            CHECK(!obj["source"].toString().isEmpty());
            CHECK(!obj["target"].toString().isEmpty());
            for (const auto& entry : obj["files"].toArray()) {
                CHECK(entry.toObject()["path"].toString().startsWith("..") == false);
            }
        }

        // Source stays byte-identical: migration reads, never writes.
        CHECK(hashFile(source.filePath("throne.db")) == sourceDbHashBefore);
        CHECK(hashFile(source.filePath("config/profiles.json")) == QCryptographicHash::hash(R"([{"tag":"example"}])", QCryptographicHash::Sha256));
        // The migrated database is a readable SQLite snapshot of the source.
        CHECK(markerRowReadable(target.filePath("throne.db"), "PC-TEST"));
        CHECK(markerRowReadable(source.filePath("throne.db"), "PC-TEST"));
        return failures;
    }

    int testMigrateRefusesNonFreshTarget(const QDir& source) {
        int failures = 0;
        QTemporaryDir target;
        CHECK(target.isValid());
        writeFile(target.filePath("throne.db"), "already-here");

        const auto result = ProxyCore::Storage::MigrateFromThrone(source.absolutePath(), target.path());
        CHECK(result.ok == false);
        CHECK(result.error.isEmpty() == false);
        CHECK(hashFile(target.filePath("throne.db")) == QCryptographicHash::hash("already-here", QCryptographicHash::Sha256));
        CHECK(QFile::exists(target.filePath("migration-manifest.json")) == false);
        return failures;
    }

    int testMigrateRefusesNonThroneSource() {
        int failures = 0;
        QTemporaryDir source, target;
        CHECK(source.isValid() && target.isValid());
        writeFile(source.filePath("random.txt"), "not a throne dir");

        const auto result = ProxyCore::Storage::MigrateFromThrone(source.path(), target.path());
        CHECK(result.ok == false);
        CHECK(QFile::exists(target.filePath("migration-manifest.json")) == false);
        CHECK(QDir(target.filePath("migration-staging")).exists() == false);
        return failures;
    }

    int testRollback(const QDir& source) {
        int failures = 0;
        QTemporaryDir target;
        CHECK(target.isValid());
        CHECK(ProxyCore::Storage::MigrateFromThrone(source.absolutePath(), target.path()).ok);

        const auto rollback = ProxyCore::Storage::RollbackMigration(target.path());
        CHECK(rollback.ok);
        CHECK(rollback.filesRemoved == 4);
        CHECK(QFile::exists(target.filePath("throne.db")) == false);
        CHECK(QFile::exists(target.filePath("config/profiles.json")) == false);
        CHECK(QFile::exists(target.filePath("migration-manifest.json")) == false);

        // Second rollback has nothing to do and refuses cleanly.
        const auto second = ProxyCore::Storage::RollbackMigration(target.path());
        CHECK(second.ok == false);

        // The Throne source is untouched by rollback as well.
        CHECK(QFile::exists(source.filePath("throne.db")));
        return failures;
    }

    int testStaleStagingRemoved(const QDir& source) {
        int failures = 0;
        QTemporaryDir target;
        CHECK(target.isValid());
        writeFile(target.filePath("migration-staging/leftover.txt"), "junk");

        const auto result = ProxyCore::Storage::MigrateFromThrone(source.absolutePath(), target.path());
        CHECK(result.ok);
        CHECK(QDir(target.filePath("migration-staging")).exists() == false);
        return failures;
    }

    // A Throne directory whose database is currently held open by a writer
    // connection in WAL mode: the committed row lives in throne.db-wal, not
    // in throne.db. The migration must still land a readable database at the
    // target and must never ship -wal/-shm sidecars, while the live source
    // (including the open writer) stays untouched.
    int testMigrateLiveWalDatabase() {
        int failures = 0;
        QTemporaryDir source;
        CHECK(source.isValid());
        const QString dbPath = source.filePath("throne.db");
        {
            SQLite::Database db(dbPath.toStdString(), SQLite::OPEN_READWRITE | SQLite::OPEN_CREATE);
            db.exec("PRAGMA journal_mode=WAL");
            db.exec("CREATE TABLE marker(v TEXT)");
        }
        // Held open across the migration on purpose: no checkpoint happens,
        // so the committed row only exists in the WAL sidecar.
        SQLite::Database writer(dbPath.toStdString(), SQLite::OPEN_READWRITE);
        writer.exec("INSERT INTO marker VALUES('PC-TEST')");
        CHECK(QFile::exists(source.filePath("throne.db-wal")));

        QTemporaryDir target;
        CHECK(target.isValid());
        const auto result = ProxyCore::Storage::MigrateFromThrone(source.path(), target.path());
        CHECK(result.ok);
        CHECK(result.filesCopied == 1);
        CHECK(QFile::exists(target.filePath("throne.db")));
        CHECK(QFile::exists(target.filePath("throne.db-wal")) == false);
        CHECK(QFile::exists(target.filePath("throne.db-shm")) == false);
        CHECK(markerRowReadable(target.filePath("throne.db"), "PC-TEST"));

        // The live source is still intact and the writer still works.
        CHECK(markerRowReadable(source.filePath("throne.db"), "PC-TEST"));
        writer.exec("INSERT INTO marker VALUES('PC-TEST-2')");
        return failures;
    }

    int testCoexistence() {
        // The data-dir contract Throne and ProxyCore both rely on: application
        // name selects the data directory, so two installed apps with
        // different names never share one. Throne keeps its data in the
        // "config" subdirectory of its data dir.
        int failures = 0;
        const QString ours = QStandardPaths::writableLocation(QStandardPaths::AppConfigLocation);
        CHECK(ours.endsWith("ProxyCore"));
        const QString throne = ProxyCore::Storage::DefaultThroneDataDir();
        CHECK(throne.endsWith("Throne/config"));
        CHECK(QDir(throne).absolutePath() != QDir(ours).absolutePath());
        CHECK(QDir(QFileInfo(throne).absolutePath()).dirName() == QString("Throne"));
        return failures;
    }

}

int main(int argc, char* argv[]) {
    QCoreApplication app(argc, argv);
    QCoreApplication::setApplicationName("ProxyCore");

    int failures = 0;

    QTemporaryDir source;
    if (!source.isValid()) {
        qWarning("cannot create temp source dir");
        return 2;
    }
    makeThroneSource(QDir(source.path()));

    failures += testMigrateFreshCopy(QDir(source.path()));
    failures += testMigrateRefusesNonFreshTarget(QDir(source.path()));
    failures += testMigrateRefusesNonThroneSource();
    failures += testRollback(QDir(source.path()));
    failures += testStaleStagingRemoved(QDir(source.path()));
    failures += testMigrateLiveWalDatabase();
    failures += testCoexistence();

    if (failures == 0) {
        qInfo("proxycore_tests: all checks passed");
        return 0;
    }
    qWarning("proxycore_tests: %d check(s) failed", failures);
    return 1;
}
