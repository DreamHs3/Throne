// REC-03: service-mode support for the existing GUI. The runtime lives
// inside the ProxyCoreService; the GUI drives it through API::ServiceClient
// (PC-110 envelope protocol) and never spawns a child core, never falls back
// to one silently. Everything here mirrors the classification of
// docs/recovery/REC-03.md §3: transport problems, envelope-level refusals
// and errors inside the typed payload are reported differently, and an
// UNKNOWN Start/Stop outcome is resolved through Health — never retried.

#include "include/ui/mainwindow.h"

#include "include/api/ServiceClient.h"
#include "include/database/DatabaseManager.h"
#include "include/global/Configs.hpp"
#include "include/global/RunningProfiles.hpp"
#include "include/stats/traffic/TrafficLooper.hpp"
#include "include/ui/stats/dialog_endpoint_details.h"
#include "include/ui/utils/MessageBoxTimer.h"

#include <QMessageBox>

namespace {
    // QTimer runs on the UI thread; the probe itself hops to a worker.
    constexpr int kServiceHealthIntervalMs = 2000;
    constexpr int kServiceHealthTimeoutMs = 8000;
} // namespace

bool MainWindow::service_mode_active() const {
    return Configs::dataManager->settingsRepo->service_mode;
}

void MainWindow::start_service_health_monitor() {
    if (m_serviceHealthTimer != nullptr) return;
    m_serviceHealthTimer = new QTimer(this);
    m_serviceHealthTimer->setInterval(kServiceHealthIntervalMs);
    connect(m_serviceHealthTimer, &QTimer::timeout, this, [this] {
        runOnNewThread([this] {
            API::ServiceClient::HealthInfo info{};
            const auto r = API::defaultServiceClient->HealthTry(&info, kServiceHealthTimeoutMs);
            runOnUiThread([=, this] {
                service_health_tick(r.outcome, info.runtimeRunning, r.message);
            });
        });
    });
    m_serviceHealthTimer->start();
}

// UI thread only.
void MainWindow::service_health_tick(API::ServiceClient::Outcome outcome, bool runtimeRunning,
                                     const QString &message) {
    if (!service_mode_active()) return;
    int state;
    switch (outcome) {
        case API::ServiceClient::Outcome::Ok: state = runtimeRunning ? 1 : 0; break;
        case API::ServiceClient::Outcome::Busy: return; // an operation holds the client; next tick
        case API::ServiceClient::Outcome::AccessDenied: state = 3; break;
        case API::ServiceClient::Outcome::ConnectFailed: state = 2; break;
        default: state = 4; break; // transport/timeout/protocol breakage
    }
    if (state == m_serviceLastHealthState) return;
    const bool firstReading = m_serviceLastHealthState < 0;
    m_serviceLastHealthState = state;

    switch (state) {
        case 0:
            MW_show_log(tr("[Service] service reachable; no runtime is running."));
            break;
        case 1:
            MW_show_log(tr("[Service] service reachable; a runtime is running."));
            break;
        case 2:
            MW_show_log(tr("[Service] the service is not reachable: %1").arg(message));
            break;
        case 3:
            MW_show_log(tr("[Service] access to the service pipe is denied."));
            break;
        default:
            MW_show_log(tr("[Service] service communication problem: %1").arg(message));
            break;
    }

    // Reconcile the GUI state with the service truth. A stop through another
    // client (or a runtime crash) must not leave a stale running profile.
    // An in-flight start/stop holds its mutex: skip reconciliation then.
    if (outcome != API::ServiceClient::Outcome::Ok) return;
    if (!runtimeRunning && running != nullptr) {
        if (!mu_starting.tryLock()) return; // a start is in flight
        mu_starting.unlock();
        if (!mu_stopping.tryLock()) return; // a stop is in flight
        mu_stopping.unlock();
        MW_show_log(tr("[Service] the service reports the runtime has stopped; updating the GUI state."));
        Configs::dataManager->settingsRepo->UpdateStartedId(Configs::NoProfileId);
        Stats::SetVpnEndpointProfiles({});
        Configs::ClearRunningProfiles();
        running = nullptr;
        refresh_status();
    } else if (runtimeRunning && running == nullptr && !firstReading) {
        MW_show_log(tr("[Service] the service reports a running runtime (started outside this GUI?)."));
    }
}

// Worker thread (profile_start_stage2). Returns false when the start must
// stop here; every failure has already been reported explicitly.
bool MainWindow::service_check_config_then_start(const libcore::LoadConfigReq &req, QString *payloadError) {
    auto *svc = API::defaultServiceClient;

    MW_show_log(tr("[Service] checking the configuration through the service..."));
    const auto cc = svc->CheckConfig(req, 60000);
    if (!cc.ok()) {
        if (cc.outcome == API::ServiceClient::Outcome::PayloadError) {
            MW_show_log(tr("[Service] CheckConfig error: %1").arg(cc.message));
            runOnUiThread([=, this] {
                MessageBoxWarning(tr("CheckConfig return error"), cc.message);
            });
        } else if (cc.outcome == API::ServiceClient::Outcome::EnvelopeError) {
            MW_show_log(tr("[Service] CheckConfig refused by the service (code %1): %2").arg(cc.envelopeCode).arg(cc.message));
            runOnUiThread([=, this] {
                MessageBoxWarning(tr("Service refused CheckConfig"),
                                  tr("Code %1: %2").arg(cc.envelopeCode).arg(cc.message));
            });
        } else {
            service_report_operation_failure("CheckConfig", cc);
            service_report_unknown("CheckConfig", cc);
        }
        return false;
    }

    // The request is sent EXACTLY once: a transport problem here leaves the
    // outcome UNKNOWN and the state is resolved through Health, never by a
    // silent resend.
    const auto st = svc->Start(req, 60000);
    if (st.ok()) {
        payloadError->clear();
        return true;
    }
    if (st.outcome == API::ServiceClient::Outcome::PayloadError) {
        *payloadError = st.message;
        return true; // the legacy error chain (geo assets, tun, ...) reports it
    }
    service_report_operation_failure("Start", st);
    if (!st.outcomeKnown()) service_report_unknown("Start", st);
    return false;
}

// Worker thread (profile_stop_stage2).
bool MainWindow::service_stop() {
    const auto r = API::defaultServiceClient->Stop(15000);
    if (r.ok()) return true;
    if (r.outcome == API::ServiceClient::Outcome::PayloadError) {
        runOnUiThread([=, this] {
            MessageBoxWarning(tr("Stop return error"), r.message);
        });
        return false;
    }
    service_report_operation_failure("Stop", r);
    if (!r.outcomeKnown()) service_report_unknown("Stop", r);
    return false;
}

// Worker thread. Classifies a non-payload failure of an operation for the log.
void MainWindow::service_report_operation_failure(const QString &what, const API::ServiceClient::Result &r) {
    QString text;
    switch (r.outcome) {
        case API::ServiceClient::Outcome::AccessDenied:
            text = tr("%1: access to the service pipe is denied (run the GUI as the user the service was installed for).")
                       .arg(what);
            break;
        case API::ServiceClient::Outcome::ConnectFailed:
            text = tr("%1: the service is not reachable (%2).").arg(what, r.message);
            break;
        case API::ServiceClient::Outcome::EnvelopeError:
            text = tr("%1: refused by the service (code %2): %3").arg(what).arg(r.envelopeCode).arg(r.message);
            break;
        case API::ServiceClient::Outcome::Timeout:
            text = tr("%1: the service did not answer within the deadline; the outcome is UNKNOWN.").arg(what);
            break;
        case API::ServiceClient::Outcome::TransportError:
            text = tr("%1: the connection to the service broke; the outcome is UNKNOWN.").arg(what);
            break;
        default:
            text = tr("%1: service communication problem: %2").arg(what, r.message);
            break;
    }
    MW_show_log(QStringLiteral("[Service] ") + text);
    if (r.outcome == API::ServiceClient::Outcome::AccessDenied || r.outcome == API::ServiceClient::Outcome::ConnectFailed) {
        runOnUiThread([=, this] {
            MessageBoxWarning(tr("Service unavailable"), text);
        });
    }
}

// Worker thread: resolves an UNKNOWN Start/Stop outcome through a Health
// probe (which reconnects and re-handshakes) and reports the actual state.
void MainWindow::service_report_unknown(const QString &what, const API::ServiceClient::Result &r) {
    API::ServiceClient::HealthInfo info{};
    const auto h = API::defaultServiceClient->Health(&info, kServiceHealthTimeoutMs);
    QString state;
    if (h.ok()) {
        state = info.runtimeRunning ? tr("the service reports a RUNNING runtime")
                                    : tr("the service reports no runtime");
    } else {
        state = tr("the service is not reachable");
    }
    MW_show_log(tr("[Service] %1: the outcome is UNKNOWN (%2). Establishing the state: %3. "
                   "The command is NOT retried automatically.")
                    .arg(what, r.message, state));
}
