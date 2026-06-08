cmake_minimum_required(VERSION 3.16)
project(cliproxy_usage_c C)

add_library(cliproxy_usage_c SHARED src/plugin.c)
set_target_properties(cliproxy_usage_c PROPERTIES
    OUTPUT_NAME "usage-c"
    PREFIX ""
)
