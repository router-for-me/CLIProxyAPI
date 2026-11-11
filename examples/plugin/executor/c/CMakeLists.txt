cmake_minimum_required(VERSION 3.16)
project(cliproxy_executor_c C)

add_library(cliproxy_executor_c SHARED src/plugin.c)
set_target_properties(cliproxy_executor_c PROPERTIES
    OUTPUT_NAME "executor-c"
    PREFIX ""
)
