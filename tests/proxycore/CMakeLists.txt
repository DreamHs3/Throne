# ProxyCore test harnesses (PC-010/PC-030). Only built when
# PROXYCORE_BUILD_TESTS=ON; the default upstream build is untouched.

enable_testing()

# --- PC-010 migration/coexistence (QtCore only) ---
# SQLiteCpp is linked in so the harness creates real SQLite databases and the
# migration code exercises its consistent snapshot path.
add_executable(proxycore_tests
        test_throne_migration.cpp
        ${CMAKE_SOURCE_DIR}/src/proxycore/storage/ThroneMigration.cpp
        ${CMAKE_SOURCE_DIR}/include/proxycore/storage/ThroneMigration.h
        ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/src/Backup.cpp
        ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/src/Column.cpp
        ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/src/Database.cpp
        ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/src/Exception.cpp
        ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/src/Savepoint.cpp
        ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/src/Statement.cpp
        ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/src/Transaction.cpp
        ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/src/sqlite3.c
)
target_include_directories(proxycore_tests PRIVATE
        ${CMAKE_SOURCE_DIR}
        ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/include
)
target_link_libraries(proxycore_tests PRIVATE Qt6::Core)
add_test(NAME proxycore_migration COMMAND proxycore_tests)

# --- PC-030 characterization (whole non-UI application core) ---
# Links the app's global layer, config compiler/parser layer and database
# layer against a real temp SQLite database, so RouteRule/RouteProfile/
# RoutesRepo behavior and the protocol parser fixtures are exercised with the
# production code, not copies. UI is not linked; the three MainWindow members
# referenced from the core come from test_stubs.cpp.
include(sources_app_core.cmake)
# The list is repo-root-relative (mechanical extraction from root
# PROJECT_SOURCES) but it is consumed from tests/proxycore scope, so resolve
# every relative entry against the repo root. ${QV2RAY_RC} (Windows version
# resource + generated VersionInfo.h, absolute paths into THIS build dir,
# empty off-Windows) must survive verbatim: unquoted on purpose — a quoted
# multi-valued variable would match nothing. Test binaries do not need the
# resource, but keeping it keeps the link closest to the app.
list(REMOVE_ITEM PROXYCORE_APP_CORE_SOURCES ${QV2RAY_RC})
list(TRANSFORM PROXYCORE_APP_CORE_SOURCES PREPEND "${CMAKE_SOURCE_DIR}/")
list(APPEND PROXYCORE_APP_CORE_SOURCES ${QV2RAY_RC})
# src/configs/outbounds/masque.cpp is in root PROJECT_SOURCES (upstream
# masque support) but missing from the extracted list; appended here (absolute
# path, before add_executable consumes the list) so the generated file stays
# pristine.
list(APPEND PROXYCORE_APP_CORE_SOURCES "${CMAKE_SOURCE_DIR}/src/configs/outbounds/masque.cpp")
# include/ui/mainwindow.ui is a UI design document: it contributes no symbols
# to logic tests, but with AUTOUIC on it would emit a colliding
# ui_mainwindow.h per harness executable. Dropped from the harness list
# (upstream build untouched); AUTOUIC is off below for the same reason.
# (Placed after the root-prepend above, hence the absolute form.)
list(REMOVE_ITEM PROXYCORE_APP_CORE_SOURCES "${CMAKE_SOURCE_DIR}/include/ui/mainwindow.ui")
# Each TU below is a standalone runner with its own main(): one executable per
# TU (bundling both would fail with duplicate-main). Both link the same
# app-core seam + stubs + generated bindings; both run from the repo root
# (the parser suite reads tests/fixtures relatively).
# include/ui/mainwindow.h includes its uic output (ui_mainwindow.h), but
# AUTOUIC is off (see below), so generate it ONCE here and share the output
# between both harnesses — generating per-target would collide two rules on
# one output file.
qt6_wrap_ui(MAINWINDOW_UI_GENERATED ${CMAKE_SOURCE_DIR}/include/ui/mainwindow.ui)
function(add_characterization_harness target main_cpp)
    add_executable(${target}
            ${main_cpp}
            test_stubs.cpp
            ${MAINWINDOW_UI_GENERATED}
            ${PROXYCORE_APP_CORE_SOURCES}
    )
    set_property(TARGET ${target} PROPERTY AUTOMOC ON)
    # No AUTOUIC: no compiled source includes ui_*.h (verified by sweep), and
    # the .ui would otherwise emit a colliding ui_mainwindow.h per executable.
    # AUTORCC stays on for the bundled .qrc (per-target outputs, no collision).
    set_property(TARGET ${target} PROPERTY AUTORCC ON)
    target_include_directories(${target} PRIVATE
            ${CMAKE_SOURCE_DIR}
            ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/include
            ${CMAKE_BINARY_DIR}
            # uic output dir (ui_mainwindow.h for mainwindow.h).
            ${CMAKE_CURRENT_BINARY_DIR}
    )
    target_link_libraries(${target} PRIVATE
            Qt6::Widgets Qt6::Network
            Threads::Threads
            myproto
    )
    if (WIN32)
        target_compile_definitions(${target} PRIVATE UNICODE _UNICODE NOMINMAX _WIN32_WINNT=0x600)
        if (MSVC)
            target_compile_options(${target} PRIVATE "/utf-8")
        endif ()
        target_link_libraries(${target} PRIVATE
                wininet wsock32 ws2_32 user32 rasapi32 iphlpapi ntdll wbemuuid psapi shell32)
    endif ()
    # include/ui/mainwindow.h is pulled in by several linked sources (for the
    # stubbed members in test_stubs.cpp) but the full UI — every slot plus the
    # ctor — is intentionally not linked. Register the header with the target
    # and mark it skipped so AUTOMOC does not emit its meta-object (~30
    # undefined refs).
    target_sources(${target} PRIVATE
            ${CMAKE_SOURCE_DIR}/include/ui/mainwindow.h)
    set_source_files_properties(${CMAKE_SOURCE_DIR}/include/ui/mainwindow.h
            PROPERTIES HEADER_FILE_ONLY ON SKIP_AUTOMOC ON)
    add_test(NAME ${target}
            COMMAND ${target}
            WORKING_DIRECTORY ${CMAKE_SOURCE_DIR})
endfunction()
add_characterization_harness(proxycore_characterization test_route_characterization.cpp)
add_characterization_harness(proxycore_parser_fixtures test_parser_fixtures.cpp)

# --- REC-03: service pipe client (QtCore/QtNetwork + generated envelopes) ---
# Links ONLY the client (no UI, no app-core): the fake pipe server inside the
# test speaks the same PC-110 envelope protocol via the generated bindings.
# Must run WITHOUT the real ProxyCoreService running: the harness binds the
# fixed production pipe name.
add_executable(proxycore_service_client
        test_service_client.cpp
        ${CMAKE_SOURCE_DIR}/src/api/ServiceClient.cpp
        ${CMAKE_SOURCE_DIR}/include/api/ServiceClient.h
)
set_property(TARGET proxycore_service_client PROPERTY AUTOMOC ON)
target_include_directories(proxycore_service_client PRIVATE
        ${CMAKE_SOURCE_DIR}
        # Generated bindings: spb generation writes core/server/gen/libcore.pb.h
        # into the root binary directory (myproto.cmake is include()d there).
        ${CMAKE_BINARY_DIR}
)
target_link_libraries(proxycore_service_client PRIVATE
        Qt6::Network
        myproto
)
if (WIN32)
    target_compile_definitions(proxycore_service_client PRIVATE UNICODE _UNICODE NOMINMAX _WIN32_WINNT=0x600)
    if (MSVC)
        target_compile_options(proxycore_service_client PRIVATE "/utf-8")
    endif ()
endif ()
add_test(NAME proxycore_service_client
         COMMAND proxycore_service_client
         WORKING_DIRECTORY ${CMAKE_SOURCE_DIR})
add_test(NAME proxycore_no_updater_guard
         COMMAND ${CMAKE_COMMAND} -E env bash tests/proxycore/check_no_updater.sh
         WORKING_DIRECTORY ${CMAKE_SOURCE_DIR})
