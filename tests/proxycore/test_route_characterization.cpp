// PC-030 characterization tests: capture the CURRENT behavior of the routing
// model (RouteRule / RouteProfile / RoutesRepo) of the Throne baseline so
// later packages can prove they did not change it. Expected values encode
// existing behavior — including quirks — not a desired architecture.
//
// Runs against a real temp SQLite database created through the production
// Configs::initDB path. No GUI, no core process, no network.

#include <QCoreApplication>
#include <QDir>
#include <QFile>
#include <QFileInfo>
#include <QJsonArray>
#include <QJsonDocument>
#include <QJsonObject>
#include <QRegularExpression>
#include <QTemporaryDir>

#include "include/global/Configs.hpp"
#include "include/database/DatabaseManager.h"
#include "include/database/entities/RouteRule.h"
#include "include/database/entities/RouteProfile.h"
#include "include/database/RoutesRepo.h"

#define CHECK(cond)                                                                        \
    do {                                                                                   \
        if (!(cond)) {                                                                     \
            qWarning("CHECK failed at %s:%d: %s", __FILE__, __LINE__, #cond);              \
            failures++;                                                                    \
        }                                                                                  \
    } while (false)

using namespace Configs;

namespace {

    int failures = 0;

    // ---- helpers -----------------------------------------------------------

    QJsonObject toJson(RouteRule& rule, bool forView = false, const QString& tag = QString()) {
        return rule.get_rule_json(forView, tag);
    }

    // ---- condition fields --------------------------------------------------

    void testConditionFields() {
        // process name / path / regex (ProxyCore product terms: process_name,
        // process_path; serialization belongs to the upstream model).
        {
            RouteRule r;
            r.process_name << "chrome.exe";
            r.outboundID = proxyID;
            auto j = toJson(r);
            CHECK(j["process_name"].toArray() == QJsonArray{"chrome.exe"});
            CHECK(j["action"] == "route");
            // forView=false without a tag serializes the outbound as the raw int id.
            CHECK(j["outbound"] == -1);
        }
        {
            RouteRule r;
            r.process_path << "C:/Program Files/Google/Chrome/chrome.exe";
            r.outboundID = directID;
            auto j = toJson(r);
            CHECK(j["process_path"].toArray() == QJsonArray{"C:/Program Files/Google/Chrome/chrome.exe"});
            CHECK(j["outbound"] == -2);
        }
        {
            RouteRule r;
            r.process_path_regex << "^C:/Program Files/.*chrome\\.exe$";
            auto j = toJson(r);
            CHECK(j["process_path_regex"].toArray() == QJsonArray{"^C:/Program Files/.*chrome\\.exe$"});
        }
        // domain family
        {
            RouteRule r;
            r.domain << "example.com";
            r.domain_suffix << "steamcontent.com";
            r.domain_keyword << "akamai";
            r.domain_regex << ".*\\.example\\.org";
            auto j = toJson(r);
            CHECK(j["domain"].toArray() == QJsonArray{"example.com"});
            CHECK(j["domain_suffix"].toArray() == QJsonArray{"steamcontent.com"});
            CHECK(j["domain_keyword"].toArray() == QJsonArray{"akamai"});
            CHECK(j["domain_regex"].toArray() == QJsonArray{".*\\.example\\.org"});
        }
        // destination IP/CIDR + private shortcuts + source
        {
            RouteRule r;
            r.ip_cidr << "203.0.113.0/24" << "2001:db8::/32";
            r.ip_is_private = true;
            r.source_ip_cidr << "198.51.100.7/32";
            r.source_ip_is_private = false;
            auto j = toJson(r);
            CHECK((j["ip_cidr"].toArray() == QJsonArray{"203.0.113.0/24", "2001:db8::/32"}));
            CHECK(j["ip_is_private"] == true);
            CHECK(j["source_ip_cidr"].toArray() == QJsonArray{"198.51.100.7/32"});
            // false booleans are omitted, not written.
            CHECK(j.contains("source_ip_is_private") == false);
        }
        // port / port_range: ports cast to numbers, ranges stay strings.
        {
            RouteRule r;
            r.port << "443" << " 8080 ";
            r.port_range << "1000:2000";
            r.source_port << "53";
            r.source_port_range << "60000:61000";
            auto j = toJson(r);
            CHECK((j["port"].toArray() == QJsonArray{443, 8080}));
            CHECK(j["port_range"].toArray() == QJsonArray{"1000:2000"});
            CHECK(j["source_port"].toArray() == QJsonArray{53});
            CHECK(j["source_port_range"].toArray() == QJsonArray{"60000:61000"});
        }
        // network / protocol / ip_version / invert
        {
            RouteRule r;
            r.network = " tcp ";
            r.protocol = "dns";
            r.ip_version = "6";
            r.invert = true;
            auto j = toJson(r);
            CHECK(j["network"] == "tcp");   // scalar fields are trimmed
            CHECK(j["protocol"] == "dns");
            CHECK(j["ip_version"] == 6);    // ip_version is cast to a number
            CHECK(j["invert"] == true);
        }
        // empty condition lists must not appear at all
        {
            RouteRule r;
            r.domain << "";
            r.domain << "  ";
            r.outboundID = directID;
            auto j = toJson(r);
            CHECK(j.contains("domain") == false);
        }
    }

    // ---- actions -----------------------------------------------------------

    void testActions() {
        // Direct/Proxy/Block int ids and their view strings.
        {
            RouteRule r;
            r.outboundID = proxyID;
            CHECK(toJson(r)["outbound"] == -1);
            CHECK(toJson(r, true)["outbound"] == "proxy");
        }
        {
            RouteRule r;
            r.outboundID = directID;
            CHECK(toJson(r)["outbound"] == -2);
            CHECK(toJson(r, true)["outbound"] == "direct");
        }
        // Block: route+blockID turns into reject — AND mutates the rule object
        // (documented upstream quirk: serialization has a side effect).
        {
            RouteRule r;
            r.outboundID = blockID;
            r.action = "route";
            CHECK(r.action == "route");
            auto j = toJson(r);
            CHECK(j["action"] == "reject");
            CHECK(r.action == "reject");     // M02: side effect captured
        }
        // reject with method / no_drop
        {
            RouteRule r;
            r.action = "reject";
            r.rejectMethod = " drop ";
            r.no_drop = true;
            auto j = toJson(r);
            CHECK(j["action"] == "reject");
            CHECK(j["reject_method"] == "drop");
            CHECK(j["no_drop"] == true);
        }
        // hijack-dns survives serialization unchanged.
        {
            RouteRule r;
            r.action = "hijack-dns";
            r.protocol = "dns";
            auto j = toJson(r);
            CHECK(j["action"] == "hijack-dns");
            CHECK(j["protocol"] == "dns");
        }
        // route-options / override fields
        {
            RouteRule r;
            r.action = "route-options";
            r.override_address = " 127.0.0.1 ";
            r.override_port = "9";
            auto j = toJson(r);
            CHECK(j["action"] == "route-options");
            CHECK(j["override_address"] == "127.0.0.1");
            CHECK(j["override_port"] == 9);
        }
        // sniff / resolve
        {
            RouteRule r;
            r.action = "sniff";
            r.sniffOverrideDest = true;
            CHECK(toJson(r)["override_destination"] == true);
        }
        {
            RouteRule r;
            r.action = "resolve";
            r.strategy = "ipv4_first";
            CHECK(toJson(r)["strategy"] == "ipv4_first");
        }
        // outboundTag overrides the id rendering.
        {
            RouteRule r;
            r.outboundID = 42;
            CHECK(toJson(r, false, "some-vps")["outbound"] == "some-vps");
        }
    }

    // ---- rule_set naming, share json, tokens -------------------------------

    void testRuleSetAndShare() {
        // Runtime (forView=false): .srs URLs are renamed to <name>-srs-<hash>.
        {
            RouteRule r;
            r.rule_set << "https://example.com/geosite-steam.srs";
            auto j = toJson(r);
            const auto name = j["rule_set"].toArray().first().toString();
            CHECK(name.startsWith("geosite-steam-srs-"));
            CHECK(QRegularExpression("\\d+$").match(name).hasMatch());
            // View/share form preserves the raw URL.
            auto jv = toJson(r, true);
            CHECK(jv["rule_set"].toArray() == QJsonArray{"https://example.com/geosite-steam.srs"});
        }
        // share json carries name + stable type token.
        {
            RouteRule r;
            r.name = "chrome through VPS";
            r.process_name << "chrome.exe";
            r.outboundID = proxyID;
            auto share = r.to_share_json();
            CHECK(share["name"] == "chrome through VPS");
            CHECK(share["type"] == "custom");
            CHECK(share["outbound"] == "proxy");
        }
        // token round-trips for every rule type; outbound id string round-trips.
        for (int t = custom; t <= endpointPreferredBy; ++t) {
            const auto token = ruleTypeToToken(t);
            CHECK(tokenToRuleType(token) == t);
        }
        for (int id : {proxyID, directID, blockID, warpBypassID}) {
            CHECK(stringToOutboundID(outboundIDToString(id)) == id);
        }
        CHECK(outboundIDToString(-99) == "unknown");
    }

    // ---- determinism (re-serialization) ------------------------------------

    void testReserializationDeterminism() {
        RouteRule r;
        r.name = "determinism";
        r.domain_suffix << "example.com";
        r.ip_cidr << "203.0.113.0/24";
        r.port << "443";
        r.network = "tcp";
        r.outboundID = blockID; // serialization mutates action; both runs equal
        const auto a = toJson(r);
        const auto b = toJson(r);
        CHECK(a == b);
        // and the JSON is stable when re-parsed and re-emitted.
        const auto reparsed = QJsonDocument::fromJson(QJsonDocument(a).toJson()).object();
        CHECK(reparsed == a);
    }

    // ---- RouteProfile -------------------------------------------------------

    void testRouteProfile() {
        // Template simple-rule table (12 rows) with actions/outbounds.
        // get_simple_rules() itself is a private static factory, so it is
        // exercised through the public ResetSimpleRule(), which appends the
        // template row for a missing type on a fresh profile.
        RouteProfile tmpl;
        tmpl.ResetSimpleRule(simpleAddressProxy);
        tmpl.ResetSimpleRule(simpleAddressBypass);
        tmpl.ResetSimpleRule(simpleAddressBlock);
        tmpl.ResetSimpleRule(simpleProcessNameProxy);
        tmpl.ResetSimpleRule(simpleProcessNameBypass);
        tmpl.ResetSimpleRule(simpleProcessNameBlock);
        tmpl.ResetSimpleRule(simpleProcessPathProxy);
        tmpl.ResetSimpleRule(simpleProcessPathBypass);
        tmpl.ResetSimpleRule(simpleProcessPathBlock);
        tmpl.ResetSimpleRule(simpleAddressWarpBypass);
        tmpl.ResetSimpleRule(simpleProcessNameWarpBypass);
        tmpl.ResetSimpleRule(simpleProcessPathWarpBypass);
        const auto& simple = tmpl.Rules;
        CHECK(simple.size() == 12);
        for (const auto& rule : simple) {
            if (rule->type == simpleAddressBlock || rule->type == simpleProcessNameBlock || rule->type == simpleProcessPathBlock) {
                CHECK(rule->action == "reject");
            } else if (rule->type == simpleAddressWarpBypass || rule->type == simpleProcessNameWarpBypass || rule->type == simpleProcessPathWarpBypass) {
                CHECK(rule->outboundID == warpBypassID);
            } else if (rule->type == simpleAddressProxy || rule->type == simpleProcessNameProxy || rule->type == simpleProcessPathProxy) {
                CHECK(rule->outboundID == proxyID);
            } else {
                CHECK(rule->outboundID == directID);
            }
        }

        // Default chain: DNS hijack first.
        const auto def = RouteProfile::GetDefaultChain();
        CHECK(def->name == "Default");
        CHECK(def->Rules.size() == 1);
        CHECK(def->Rules.first()->action == "hijack-dns");
        CHECK(def->Rules.first()->protocol == "dns");

        // get_route_rules preserves rule ORDER and skips empty simple rules.
        RouteProfile profile;
        profile.name = "char-order";
        {
            auto r = std::make_shared<RouteRule>();
            r->name = "first-dns";
            r->action = "hijack-dns";
            r->protocol = "dns";
            profile.Rules << r;
        }
        {
            auto r = std::make_shared<RouteRule>();
            r->type = simpleProcessNameProxy; // empty simple rule -> skipped
            profile.Rules << r;
        }
        {
            auto r = std::make_shared<RouteRule>();
            r->name = "second-block";
            r->process_path << "C:/apps/test-client.exe";
            r->outboundID = blockID;
            profile.Rules << r;
        }
        {
            auto r = std::make_shared<RouteRule>();
            r->name = "third-direct";
            r->process_path << "C:/Program Files/Steam/steam.exe";
            r->outboundID = directID;
            profile.Rules << r;
        }
        const auto rulesJson = profile.get_route_rules(false);
        CHECK(rulesJson.size() == 3);
        if (rulesJson.size() == 3) {
            CHECK(rulesJson.at(0).toObject()["action"] == "hijack-dns");
            CHECK(rulesJson.at(1).toObject()["process_path"].toArray() == QJsonArray{"C:/apps/test-client.exe"});
            CHECK(rulesJson.at(2).toObject()["process_path"].toArray() == QJsonArray{"C:/Program Files/Steam/steam.exe"});
            CHECK(rulesJson.at(1).toObject()["action"] == "reject");
            CHECK(rulesJson.at(2).toObject()["outbound"] == -2);
        }

        // adblock: enabled setting injects one reject rule before the first
        // route-action rule (service rule ordering contract). Here that is
        // index 2: hijack-dns and reject render first, third-direct renders
        // action "route" (default action, outbound directID).
        const bool adblockOriginal = dataManager->settingsRepo->adblock_enable;
        dataManager->settingsRepo->adblock_enable = true;
        const auto withAdblock = profile.get_route_rules(false);
        dataManager->settingsRepo->adblock_enable = adblockOriginal;
        CHECK(withAdblock.size() == 4);
        if (withAdblock.size() == 4) {
            CHECK(withAdblock.at(2).toObject()["rule_set"].toArray() == QJsonArray{"throne-adblocksingbox"});
            CHECK(withAdblock.at(2).toObject()["action"] == "reject");
        }

        // A profile with no route-action rules still gets the adblock rule at the end.
        RouteProfile noRoute;
        {
            auto r = std::make_shared<RouteRule>();
            r->name = "only-dns";
            r->action = "hijack-dns";
            r->protocol = "dns";
            noRoute.Rules << r;
        }
        dataManager->settingsRepo->adblock_enable = true;
        const auto tailAdblock = noRoute.get_route_rules(false);
        dataManager->settingsRepo->adblock_enable = adblockOriginal;
        CHECK(tailAdblock.size() == 2);
        if (tailAdblock.size() == 2) {
            CHECK(tailAdblock.at(1).toObject()["rule_set"].toArray() == QJsonArray{"throne-adblocksingbox"});
        }
    }

    // ---- RoutesRepo persistence round-trip ---------------------------------

    void testRoutesRepoRoundTrip() {
        auto& repo = dataManager->routesRepo;

        // Create, persist, reload, compare the compiled rule projection.
        auto profile = std::make_shared<RouteProfile>();
        profile->name = "char-repo-roundtrip";
        {
            auto r = std::make_shared<RouteRule>();
            r->name = "steam direct";
            r->type = simpleProcessPathBypass;
            r->process_path << "C:/Program Files/Steam/steam.exe";
            r->outboundID = directID;
            profile->Rules << r;
        }
        {
            auto r = std::make_shared<RouteRule>();
            r->name = "test client block";
            r->process_name << "test-client.exe";
            r->outboundID = blockID;
            profile->Rules << r;
        }
        {
            auto r = std::make_shared<RouteRule>();
            r->name = "domain port";
            r->domain_suffix << "example.com";
            r->port << "8443";
            r->network = "tcp";
            r->outboundID = proxyID;
            profile->Rules << r;
        }
        CHECK(repo->AddRouteProfile(profile));
        CHECK(profile->id >= 0);

        const auto loaded = repo->GetRouteProfile(profile->id);
        CHECK(loaded != nullptr);
        if (loaded) {
            CHECK(loaded->name == "char-repo-roundtrip");
            CHECK(loaded->Rules.size() == 3);
            // The db round-trip preserves the compiled projection.
            const auto before = profile->get_route_rules(false);
            const auto after = loaded->get_route_rules(false);
            CHECK(before == after);
            // field-level: ids/actions survive
            CHECK(loaded->Rules.at(1)->outboundID == blockID);
            CHECK(loaded->Rules.at(2)->port == QList<QString>{"8443"});
        }

        // Save over an existing profile, reload again, same projection.
        loaded->Rules.first()->process_path << "C:/Program Files/Steam/steam_exe_backup.exe";
        CHECK(repo->Save(loaded));
        const auto reloaded = repo->GetRouteProfile(profile->id);
        CHECK(reloaded->Rules.size() == 3);
        CHECK(reloaded->Rules.first()->process_path.size() == 2);

        repo->DeleteRouteProfile(profile->id);
        CHECK(repo->GetRouteProfile(profile->id) == nullptr);
    }

}

int main(int argc, char* argv[]) {
    QCoreApplication app(argc, argv);
    QCoreApplication::setApplicationName("ProxyCore");
    qputenv("QT_LOGGING_RULES", "*.debug=false");

    QTemporaryDir workDir;
    if (!workDir.isValid()) {
        qWarning("cannot create temp dir");
        return 2;
    }
    QDir::setCurrent(workDir.path());

    // Production initialization: SQLite repos in a throwaway directory.
    Configs::initDB(QDir(workDir.path()).filePath("throne.db").toStdString());

    // MW_show_log is an inline std::function global; silence it.
    MW_show_log = [](const QString&) {};

    testConditionFields();
    testActions();
    testRuleSetAndShare();
    testReserializationDeterminism();
    testRouteProfile();
    testRoutesRepoRoundTrip();

    if (failures == 0) {
        qInfo("proxycore_characterization: all checks passed");
        return 0;
    }
    qWarning("proxycore_characterization: %d check(s) failed", failures);
    return 1;
}
