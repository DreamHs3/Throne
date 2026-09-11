# ProxyCore test harnesses (PC-010/PC-030). Only built when
# PROXYCORE_BUILD_TESTS=ON; the default upstream build is untouched.

enable_testing()

add_executable(proxycore_tests
        test_throne_migration.cpp
        ${CMAKE_SOURCE_DIR}/src/proxycore/storage/ThroneMigration.cpp
        ${CMAKE_SOURCE_DIR}/include/proxycore/storage/ThroneMigration.h
)
target_include_directories(proxycore_tests PRIVATE ${CMAKE_SOURCE_DIR})
target_link_libraries(proxycore_tests PRIVATE Qt6::Core)
add_test(NAME proxycore_migration COMMAND proxycore_tests)
