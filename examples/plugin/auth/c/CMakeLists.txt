cmake_minimum_required(VERSION 3.16)
project(cliproxy_auth_c C)

add_library(cliproxy_auth_c SHARED src/plugin.c)
set_target_properties(cliproxy_auth_c PROPERTIES
    OUTPUT_NAME "auth-c"
    PREFIX ""
)
