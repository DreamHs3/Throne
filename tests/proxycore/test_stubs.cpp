// PC-030: link-time stubs for the handful of MainWindow members referenced by
// the non-UI application core (profiles/database/network collectors). The
// characterization harness links the whole non-UI source set; these members
// normally live in UI translation units that are intentionally not linked.
// No test executes them — GetMainWindow() returns nullptr in the harness.

#include "include/ui/mainwindow.h"

#include "include/global/HTTPRequestHelper.hpp"

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
