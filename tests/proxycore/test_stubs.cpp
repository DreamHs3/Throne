// PC-030: link-time stubs for the handful of MainWindow members referenced by
// the non-UI application core (profiles/database/network collectors). The
// characterization harness links the whole non-UI source set; these members
// normally live in UI translation units that are intentionally not linked.
// No test executes them — GetMainWindow() returns nullptr in the harness.

#include "include/ui/mainwindow.h"

#include "include/global/HTTPRequestHelper.hpp"

#include "include/api/RPC.h"

void MainWindow::profile_stop(bool crash, bool block, bool manual) {
    Q_UNUSED(crash)
    Q_UNUSED(block)
    Q_UNUSED(manual)
}

void MainWindow::UpdateDataView(bool force) {
    Q_UNUSED(force)
}

void MainWindow::setDownloadReport(const DownloadProgressReport& report, bool show) {
    Q_UNUSED(report)
    Q_UNUSED(show)
}

// src/api/RPC.cpp is excluded from the harness by design (it talks to a live
// core over IPC, which cannot exist in a test process). The two client calls
// reachable from linked production code are stubbed here; no test invokes
// them (nothing in the suite starts a core or registers WARP), so they only
// satisfy the linker and never execute.
QString API::Client::CheckConfig(bool* rpcOK, const QString& config, bool isXray) const {
    Q_UNUSED(rpcOK)
    Q_UNUSED(config)
    Q_UNUSED(isXray)
    return {};
}

libcore::WarpRegisterResponse API::Client::WarpRegister(bool* rpcOK, const QString& tunnelType, const QString& proxy) {
    Q_UNUSED(rpcOK)
    Q_UNUSED(tunnelType)
    Q_UNUSED(proxy)
    return {};
}

// qobject_cast<MainWindow*> inside the inline GetMainWindow() (called from
// linked ProfilesRepo/HTTPRequestHelper paths) references
// MainWindow::staticMetaObject, but the full UI meta-object is intentionally
// not linked (see CMakeLists SKIP_AUTOMOC note). No test reaches those call
// sites — the harness never runs profiles or downloads, and GetMainWindow()
// is nullptr here — so a bare object satisfies the linker and is never
// dereferenced.
const QMetaObject MainWindow::staticMetaObject = {};
