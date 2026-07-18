cmake_minimum_required(VERSION 3.16)
project(cliproxy_simple_c C)

add_library(cliproxy_simple_c SHARED src/plugin.c)
set_target_properties(cliproxy_simple_c PROPERTIES
    OUTPUT_NAME "simple-c"
    PREFIX ""
)
