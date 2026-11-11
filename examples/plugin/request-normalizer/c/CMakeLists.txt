cmake_minimum_required(VERSION 3.16)
project(cliproxy_request_normalizer_c C)

add_library(cliproxy_request_normalizer_c SHARED src/plugin.c)
set_target_properties(cliproxy_request_normalizer_c PROPERTIES
    OUTPUT_NAME "request-normalizer-c"
    PREFIX ""
)
