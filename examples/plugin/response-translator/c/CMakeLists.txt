cmake_minimum_required(VERSION 3.16)
project(cliproxy_response_translator_c C)

add_library(cliproxy_response_translator_c SHARED src/plugin.c)
set_target_properties(cliproxy_response_translator_c PROPERTIES
    OUTPUT_NAME "response-translator-c"
    PREFIX ""
)
