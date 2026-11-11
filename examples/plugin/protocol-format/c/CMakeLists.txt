cmake_minimum_required(VERSION 3.16)
project(cliproxy_protocol_format_c C)

add_library(cliproxy_protocol_format_c SHARED src/plugin.c)
set_target_properties(cliproxy_protocol_format_c PROPERTIES
    OUTPUT_NAME "protocol-format-c"
    PREFIX ""
)
