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
add_executable(proxycore_characterization
        test_route_characterization.cpp
        test_parser_fixtures.cpp
        test_stubs.cpp
        ${PROXYCORE_APP_CORE_SOURCES}
)
set_property(TARGET proxycore_characterization PROPERTY AUTOMOC ON)
set_property(TARGET proxycore_characterization PROPERTY AUTOUIC ON)
set_property(TARGET proxycore_characterization PROPERTY AUTORCC ON)
set_property(TARGET proxycore_characterization PROPERTY AUTOUIC_SEARCH_PATHS "${CMAKE_SOURCE_DIR}/include/ui")
target_include_directories(proxycore_characterization PRIVATE
        ${CMAKE_SOURCE_DIR}
        ${CMAKE_SOURCE_DIR}/3rdparty/SQLiteCpp/include
)
target_link_libraries(proxycore_characterization PRIVATE
        Qt6::Widgets Qt6::Network
        Threads::Threads
)
if (WIN32)
    target_compile_definitions(proxycore_characterization PRIVATE UNICODE _UNICODE NOMINMAX _WIN32_WINNT=0x600)
    if (MSVC)
        target_compile_options(proxycore_characterization PRIVATE "/utf-8")
    endif ()
    target_link_libraries(proxycore_characterization PRIVATE
            wininet wsock32 ws2_32 user32 rasapi32 iphlpapi ntdll wbemuuid psapi shell32)
endif ()
add_test(NAME proxycore_characterization
         COMMAND proxycore_characterization
         WORKING_DIRECTORY ${CMAKE_SOURCE_DIR})
add_test(NAME proxycore_no_updater_guard
         COMMAND ${CMAKE_COMMAND} -E env bash tests/proxycore/check_no_updater.sh
         WORKING_DIRECTORY ${CMAKE_SOURCE_DIR})
