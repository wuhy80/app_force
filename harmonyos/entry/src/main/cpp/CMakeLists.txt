cmake_minimum_required(VERSION 3.16)
project(appforce_native LANGUAGES CXX)

add_library(appforce SHARED appforce_napi.cpp)
target_compile_features(appforce PRIVATE cxx_std_17)
target_compile_options(appforce PRIVATE -Wall -Wextra)
target_link_libraries(appforce PRIVATE libace_napi.z.so dl)
