cmake_minimum_required(VERSION 3.16)
project(cliproxy_model_c C)

add_library(cliproxy_model_c SHARED src/plugin.c)
set_target_properties(cliproxy_model_c PROPERTIES
    OUTPUT_NAME "model-c"
    PREFIX ""
)
