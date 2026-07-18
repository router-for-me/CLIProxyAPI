cmake_minimum_required(VERSION 3.16)
project(cliproxy_host_callback_c C)

add_library(cliproxy_host_callback_c SHARED src/plugin.c)
set_target_properties(cliproxy_host_callback_c PROPERTIES
    OUTPUT_NAME "host-callback-c"
    PREFIX ""
)
