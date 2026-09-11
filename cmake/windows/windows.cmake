set(PLATFORM_SOURCES 3rdparty/WinCommander.cpp src/sys/windows/guihelper.cpp src/sys/windows/MiniDump.cpp src/sys/windows/eventHandler.cpp src/sys/windows/WinVersion.cpp src/sys/windows/AutoRun.cpp src/sys/windows/UrlScheme.cpp)
set(PLATFORM_LIBRARIES wininet wsock32 ws2_32 user32 rasapi32 iphlpapi ntdll wbemuuid psapi shell32)

include(cmake/windows/generate_product_version.cmake)
# PC-010: visible product metadata is ProxyCore; the executable file keeps the
# internal Throne.exe name (parentcheck contract), hence ORIGINAL_FILENAME.
generate_product_version(
        QV2RAY_RC
        ICON "${CMAKE_SOURCE_DIR}/res/Throne.ico"
        NAME "ProxyCore"
        BUNDLE "ProxyCore"
        COMPANY_NAME "ProxyCore"
        COMPANY_COPYRIGHT "ProxyCore"
        FILE_DESCRIPTION "ProxyCore"
        ORIGINAL_FILENAME "Throne.exe"
        VERSION_MAJOR ${NKR_VERSION_MAJOR}
        VERSION_MINOR ${NKR_VERSION_MINOR}
        VERSION_PATCH ${NKR_VERSION_PATCH}
        VERSION_REVISION ${NKR_VERSION_REVISION}
)
add_definitions(-DUNICODE -D_UNICODE -DNOMINMAX)
set(GUI_TYPE WIN32)
if (MSVC)
    add_compile_options("/utf-8")
    add_definitions(-D_WIN32_WINNT=0x600 -D_SCL_SECURE_NO_WARNINGS -D_CRT_SECURE_NO_WARNINGS)
endif ()
