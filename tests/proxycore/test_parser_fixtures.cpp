// PC-030: execute the PC-000 parser fixtures against the production
// SSH / Shadowsocks / SOCKS5 parsers. Fixtures live in tests/fixtures and use
// only synthetic, safe data. Round-trip invariants are asserted instead of
// exact QUrl strings; documented upstream defects are asserted in their
// current (defective) form so a future fix flips the check deliberately.

#include <QCoreApplication>
#include <QDir>
#include <QFile>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QVariant>

#include "include/configs/outbounds/ssh.h"
#include "include/configs/outbounds/shadowsocks.h"
#include "include/configs/outbounds/socks.h"

#define CHECK(cond)                                                                        \
    do {                                                                                   \
        if (!(cond)) {                                                                     \
            qWarning("CHECK failed at %s:%d: %s", __FILE__, __LINE__, #cond);              \
            failures++;                                                                    \
        }                                                                                  \
    } while (false)

namespace {

    int failures = 0;

    bool readJson(const QString& relPath, QJsonObject* out) {
        QFile f(relPath);
        if (!f.open(QIODevice::ReadOnly)) {
            qWarning("cannot open fixture: %s", qUtf8Printable(relPath));
            return false;
        }
        *out = QJsonDocument::fromJson(f.readAll()).object();
        return true;
    }

    QVariant fieldSshValue(const Configs::ssh& s, const QString& name) {
        if (name == "name") return s.name;
        if (name == "server") return s.server;
        if (name == "server_port") return s.server_port;
        if (name == "user") return s.user;
        if (name == "password") return s.password;
        if (name == "private_key") return s.private_key;
        if (name == "private_key_path") return s.private_key_path;
        if (name == "private_key_passphrase") return s.private_key_passphrase;
        if (name == "client_version") return s.client_version;
        if (name == "host_key") return QVariant(s.host_key);
        return {};
    }

    QVariant fieldSsValue(const Configs::shadowsocks& s, const QString& name) {
        if (name == "name") return s.name;
        if (name == "server") return s.server;
        if (name == "server_port") return s.server_port;
        if (name == "method") return s.method;
        if (name == "password") return s.password;
        if (name == "plugin") return s.plugin;
        if (name == "plugin_opts") return s.plugin_opts;
        if (name == "uot") return s.uot;
        return {};
    }

    QVariant fieldSocksValue(const Configs::socks& s, const QString& name) {
        if (name == "name") return s.name;
        if (name == "server") return s.server;
        if (name == "server_port") return s.server_port;
        if (name == "username") return s.username;
        if (name == "password") return s.password;
        if (name == "version") return s.version;
        if (name == "uot") return s.uot;
        return {};
    }

    void checkExpected(const QJsonObject& expected,
                       const std::function<QVariant(const QString&)>& capture,
                       const QString& caseId) {
        for (auto it = expected.begin(); it != expected.end(); ++it) {
            if (it.value().isArray()) {
                CHECK(capture(it.key()) == it.value().toArray().toVariantList());
            } else {
                CHECK(capture(it.key()) == it.value().toVariant());
            }
        }
        Q_UNUSED(caseId)
    }

    void runCase(const QJsonObject& c) {
        const QString protocol = c["protocol"].toString();
        if (protocol.isEmpty()) return;
        const QString id = c["id"].toString();

        if (protocol == "ssh") {
            Configs::ssh rule;
            bool ok = c.contains("input_link") ? rule.ParseFromLink(c["input_link"].toString())
                                               : rule.ParseFromJson(c["input_json"].toObject());
            CHECK(ok == c["parse_ok"].toBool());
            if (!ok) return;
            checkExpected(c["expected"].toObject(),
                          [&](const QString& f) { return fieldSshValue(rule, f); }, id);
            if (c["round_trip_link"].toBool(true)) {
                Configs::ssh again;
                CHECK(again.ParseFromLink(rule.ExportToLink()));
                checkExpected(c["expected"].toObject(),
                              [&](const QString& f) { return fieldSshValue(again, f); }, id);
            }
            if (c["round_trip_json"].toBool(true)) {
                Configs::ssh again;
                CHECK(again.ParseFromJson(rule.ExportToJson()));
                checkExpected(c["expected"].toObject(),
                              [&](const QString& f) { return fieldSshValue(again, f); }, id);
            }
        } else if (protocol == "shadowsocks") {
            Configs::shadowsocks rule;
            bool ok = c.contains("input_link") ? rule.ParseFromLink(c["input_link"].toString())
                                               : rule.ParseFromJson(c["input_json"].toObject());
            CHECK(ok == c["parse_ok"].toBool());
            if (!ok) return;
            checkExpected(c["expected"].toObject(),
                          [&](const QString& f) { return fieldSsValue(rule, f); }, id);
            // The ss plugin case opts out of link round-trips in the fixture:
            // plugin export goes through QUrl::toPercentEncoding and the exact
            // double-encoding behavior is deliberately not pinned here.
            if (c["round_trip_link"].toBool(true)) {
                Configs::shadowsocks again;
                CHECK(again.ParseFromLink(rule.ExportToLink()));
                checkExpected(c["expected"].toObject(),
                              [&](const QString& f) { return fieldSsValue(again, f); }, id);
            }
            if (c["round_trip_json"].toBool(true)) {
                Configs::shadowsocks again;
                CHECK(again.ParseFromJson(rule.ExportToJson()));
                checkExpected(c["expected"].toObject(),
                              [&](const QString& f) { return fieldSsValue(again, f); }, id);
            }
        } else if (protocol == "socks") {
            Configs::socks rule;
            bool ok = c.contains("input_link") ? rule.ParseFromLink(c["input_link"].toString())
                                               : rule.ParseFromJson(c["input_json"].toObject());
            CHECK(ok == c["parse_ok"].toBool());
            if (!ok) return;
            checkExpected(c["expected"].toObject(),
                          [&](const QString& f) { return fieldSocksValue(rule, f); }, id);
            if (c["round_trip_link"].toBool(true)) {
                Configs::socks again;
                CHECK(again.ParseFromLink(rule.ExportToLink()));
                checkExpected(c["expected"].toObject(),
                              [&](const QString& f) { return fieldSocksValue(again, f); }, id);
            }
            if (c.contains("known_defect")) {
                // socks4-version-string-roundtrip: ExportToJson writes "4" as
                // a STRING while ParseFromJson reads toInt() — a JSON
                // round-trip of a socks4 profile currently loses version=4
                // (becomes 0). Assert the current behavior; a future fix must
                // flip this check deliberately.
                if (c["known_defect"].toString() == "socks4-version-string-roundtrip") {
                    Configs::socks again;
                    again.ParseFromJson(rule.ExportToJson());
                    CHECK(again.version == 0);
                }
                return;
            }
            if (c["round_trip_json"].toBool(true)) {
                Configs::socks again;
                CHECK(again.ParseFromJson(rule.ExportToJson()));
                checkExpected(c["expected"].toObject(),
                              [&](const QString& f) { return fieldSocksValue(again, f); }, id);
            }
        }
    }

}

int main(int argc, char* argv[]) {
    QCoreApplication app(argc, argv);
    QCoreApplication::setApplicationName("ProxyCore");

    // The ctest working directory is the repository root.
    const QString fixtureDir = QStringLiteral("tests/fixtures");

    const QStringList fixtureFiles = {"ssh.json", "shadowsocks.json", "socks.json"};
    for (const QString& fileName : fixtureFiles) {
        QJsonObject fixture;
        if (!readJson(fixtureDir + "/" + fileName, &fixture)) return 2;
        // Protocol is declared at the fixture level; per-case override wins.
        const QString fileProtocol = fixture["protocol"].toString();
        const auto cases = fixture["cases"].toArray();
        for (const auto& caseValue : cases) {
            QJsonObject c = caseValue.toObject();
            if (c["protocol"].toString().isEmpty()) c["protocol"] = fileProtocol;
            // Only parsers wired into this harness run; others are skipped.
            const QString protocol = c["protocol"].toString();
            if (protocol != "ssh" && protocol != "shadowsocks" && protocol != "socks") continue;
            runCase(c);
        }
        qInfo("fixture %s: %d case(s) executed", qUtf8Printable(fileName), cases.size());
    }

    if (failures == 0) {
        qInfo("proxycore parser fixtures: all checks passed");
        return 0;
    }
    qWarning("proxycore parser fixtures: %d check(s) failed", failures);
    return 1;
}
