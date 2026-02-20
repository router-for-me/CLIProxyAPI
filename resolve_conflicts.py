import os
import re

def resolve_conflict(file_path):
    with open(file_path, 'r', encoding='utf-8') as f:
        content = f.read()
    
    # Simple regex to find and pick the HEAD side of a conflict
    # Picks everything between <<<<<<< HEAD and =======
    # and removes everything from ======= to >>>>>>> ...
    new_content = re.sub(r'<<<<<<< HEAD\n(.*?)\n?=======\n(.*?)\n?>>>>>>>.*?\n', r'\1\n', content, flags=re.DOTALL)
    
    if new_content != content:
        with open(file_path, 'w', encoding='utf-8') as f:
            f.write(new_content)
        print(f"Resolved conflict in {file_path}")
        return True
    return False

files_to_check = [
    "./config.example.yaml",
    "./sdk/api/handlers/openai/openai_handlers.go",
    "./sdk/api/handlers/openai/openai_responses_handlers.go",
    "./internal/watcher/synthesizer/config_test.go",
    "./internal/cmd/kilo_login.go",
    "./internal/api/middleware/response_writer_test.go",
    "./pkg/llmproxy/auth/synthesizer/config_test.go",
    "./internal/api/middleware/request_logging_test.go",
    "./internal/translator/codex/openai/chat-completions/codex_openai_response.go"
]

for f in files_to_check:
    if os.path.exists(f):
        resolve_conflict(f)
    else:
        print(f"File not found: {f}")
