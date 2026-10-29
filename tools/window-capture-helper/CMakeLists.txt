cmake_minimum_required(VERSION 3.20)

project(instantcapture_window_helper LANGUAGES CXX)

enable_testing()

add_executable(instantcapture-window-helper
    src/main.cpp
)

target_compile_features(instantcapture-window-helper PRIVATE cxx_std_17)

target_compile_definitions(instantcapture-window-helper PRIVATE
    WIN32_LEAN_AND_MEAN
    NOMINMAX
    _SILENCE_CLANG_COROUTINE_MESSAGE
)

if(MSVC)
    target_compile_options(instantcapture-window-helper PRIVATE /permissive- /W4 /EHsc)
    target_link_options(instantcapture-window-helper PRIVATE /SUBSYSTEM:WINDOWS /ENTRY:mainCRTStartup)
endif()

target_link_libraries(instantcapture-window-helper PRIVATE
    d3d11
    dxgi
    user32
    ole32
    oleaut32
    runtimeobject
    uiautomationcore
    windowsapp
    windowscodecs
)

add_executable(instantcapture-window-helper-tests
    src/stitch_test.cpp
)

target_compile_features(instantcapture-window-helper-tests PRIVATE cxx_std_17)
target_compile_definitions(instantcapture-window-helper-tests PRIVATE
    WIN32_LEAN_AND_MEAN
    NOMINMAX
    _SILENCE_CLANG_COROUTINE_MESSAGE
)
target_link_libraries(instantcapture-window-helper-tests PRIVATE
    d3d11
    dxgi
    user32
    ole32
    oleaut32
    runtimeobject
    uiautomationcore
    windowsapp
    windowscodecs
)

add_test(NAME instantcapture-window-helper-stitch COMMAND instantcapture-window-helper-tests)
