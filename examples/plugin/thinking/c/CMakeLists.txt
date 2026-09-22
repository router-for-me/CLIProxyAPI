cmake_minimum_required(VERSION 3.16)
project(cliproxy_thinking_c C)

add_library(cliproxy_thinking_c SHARED src/plugin.c)
set_target_properties(cliproxy_thinking_c PROPERTIES
    OUTPUT_NAME "thinking-c"
    PREFIX ""
)
