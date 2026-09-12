#pragma once

#include <QString>

namespace ProxyCore::Storage {

    struct MigrationResult {
        bool ok = false;
        QString error;
        int filesCopied = 0;
        qint64 bytesCopied = 0;
        QString manifestPath;
    };

    struct RollbackResult {
        bool ok = false;
        QString error;
        int filesRemoved = 0;
    };

    // One-shot PC-010 migration: copies a Throne data directory into the
    // ProxyCore data directory. The source directory is only ever read —
    // a cancelled, refused or failed run must leave it byte-identical.
    //
    // Refuses (ok == false) when the source has no throne.db (not a Throne
    // data directory) or the target already has throne.db / a migration
    // manifest (already initialized or already migrated).
    MigrationResult MigrateFromThrone(const QString& sourceDir, const QString& targetConfigDir);

    // Removes exactly the files recorded in the target's migration manifest.
    // The Throne source directory is never touched. Refuses when no manifest
    // exists (nothing to roll back).
    RollbackResult RollbackMigration(const QString& targetConfigDir);

    // Default Throne data dir: the sibling "Throne/config" directory of the
    // running application's QStandardPaths::AppConfigLocation (the two differ
    // only by application name; Throne stores its data in a "config"
    // subdirectory). Portable/-appdata users pass an explicit path.
    QString DefaultThroneDataDir();

}
