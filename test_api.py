#!/usr/bin/env python3
"""
CLIProxyAPI 全面测试脚本
测试模型列表、流式输出、thinking模式及复杂任务
"""

import requests
import json
import time
import sys
import io
from typing import Optional, List, Dict, Any

# 修复 Windows 控制台编码问题
sys.stdout = io.TextIOWrapper(sys.stdout.buffer, encoding='utf-8', errors='replace')
sys.stderr = io.TextIOWrapper(sys.stderr.buffer, encoding='utf-8', errors='replace')

# 配置
BASE_URL = "http://localhost:8317"
API_KEY = "your-api-key-1"
HEADERS = {
    "Authorization": f"Bearer {API_KEY}",
    "Content-Type": "application/json"
}

# 复杂任务提示词 - 用于测试 thinking 模式
COMPLEX_TASK_PROMPT = """请帮我分析以下复杂的编程问题，并给出详细的解决方案：

问题：设计一个高并发的分布式任务调度系统，需要满足以下要求：
1. 支持百万级任务队列
2. 任务可以设置优先级、延迟执行、定时执行
3. 支持任务依赖关系（DAG调度）
4. 失败重试机制，支持指数退避
5. 任务结果持久化和查询
6. 水平扩展能力
7. 监控和告警

请从以下几个方面详细分析：
1. 整体架构设计
2. 核心数据结构
3. 调度算法选择
4. 容错机制设计
5. 性能优化策略
6. 技术选型建议

请逐步思考每个方面，给出你的推理过程。"""

# 简单测试提示词
SIMPLE_PROMPT = "Hello! Please respond with 'OK' if you receive this message."

def print_separator(title: str):
    print(f"\n{'='*60}")
    print(f"  {title}")
    print(f"{'='*60}\n")

def print_result(name: str, success: bool, detail: str = ""):
    status = "✅ PASS" if success else "❌ FAIL"
    print(f"{status} | {name}")
    if detail:
        print(f"       └─ {detail[:200]}{'...' if len(detail) > 200 else ''}")

def get_models() -> List[str]:
    """获取可用模型列表"""
    print_separator("获取模型列表")
    try:
        resp = requests.get(f"{BASE_URL}/v1/models", headers=HEADERS, timeout=30)
        if resp.status_code == 200:
            data = resp.json()
            models = [m.get("id", m.get("name", "unknown")) for m in data.get("data", [])]
            print(f"找到 {len(models)} 个模型:")
            for m in models:
                print(f"  - {m}")
            return models
        else:
            print(f"❌ 获取模型列表失败: HTTP {resp.status_code}")
            print(f"   响应: {resp.text[:500]}")
            return []
    except Exception as e:
        print(f"❌ 获取模型列表异常: {e}")
        return []

def test_model_basic(model: str) -> tuple:
    """基础可用性测试，返回 (success, error_detail)"""
    try:
        payload = {
            "model": model,
            "messages": [{"role": "user", "content": SIMPLE_PROMPT}],
            "max_tokens": 50,
            "stream": False
        }
        resp = requests.post(
            f"{BASE_URL}/v1/chat/completions",
            headers=HEADERS,
            json=payload,
            timeout=60
        )
        if resp.status_code == 200:
            data = resp.json()
            content = data.get("choices", [{}])[0].get("message", {}).get("content", "")
            return (bool(content), f"content_len={len(content)}")
        else:
            return (False, f"HTTP {resp.status_code}: {resp.text[:300]}")
    except Exception as e:
        return (False, str(e))

def test_streaming(model: str) -> Dict[str, Any]:
    """测试流式输出"""
    result = {"success": False, "chunks": 0, "content": "", "error": None}
    try:
        payload = {
            "model": model,
            "messages": [{"role": "user", "content": "Count from 1 to 5, one number per line."}],
            "max_tokens": 100,
            "stream": True
        }
        resp = requests.post(
            f"{BASE_URL}/v1/chat/completions",
            headers=HEADERS,
            json=payload,
            timeout=60,
            stream=True
        )
        
        if resp.status_code != 200:
            result["error"] = f"HTTP {resp.status_code}: {resp.text[:200]}"
            return result
        
        content_parts = []
        for line in resp.iter_lines():
            if line:
                line_str = line.decode('utf-8')
                if line_str.startswith("data: "):
                    data_str = line_str[6:]
                    if data_str.strip() == "[DONE]":
                        break
                    try:
                        data = json.loads(data_str)
                        result["chunks"] += 1
                        choices = data.get("choices", [])
                        if choices:
                            delta = choices[0].get("delta", {})
                            if "content" in delta and delta["content"]:
                                content_parts.append(delta["content"])
                    except json.JSONDecodeError:
                        pass
                    except Exception as e:
                        result["error"] = f"Parse error: {e}, data: {data_str[:200]}"
        
        result["content"] = "".join(content_parts)
        result["success"] = result["chunks"] > 0 and len(result["content"]) > 0
        
    except Exception as e:
        result["error"] = str(e)
    
    return result

def test_thinking_mode(model: str, complex_task: bool = False) -> Dict[str, Any]:
    """测试 thinking 模式"""
    result = {
        "success": False, 
        "has_reasoning": False,
        "reasoning_content": "",
        "content": "", 
        "error": None,
        "chunks": 0
    }
    
    prompt = COMPLEX_TASK_PROMPT if complex_task else "What is 15 * 23? Please think step by step."
    
    try:
        # 尝试不同的 thinking 模式参数格式
        payload = {
            "model": model,
            "messages": [{"role": "user", "content": prompt}],
            "max_tokens": 8000 if complex_task else 2000,
            "stream": True
        }
        
        # 根据模型类型添加 thinking 参数
        if "claude" in model.lower():
            payload["thinking"] = {"type": "enabled", "budget_tokens": 5000 if complex_task else 2000}
        elif "gemini" in model.lower():
            payload["thinking"] = {"thinking_budget": 5000 if complex_task else 2000}
        elif "gpt" in model.lower() or "codex" in model.lower() or "o1" in model.lower() or "o3" in model.lower():
            payload["reasoning_effort"] = "high" if complex_task else "medium"
        else:
            # 通用格式
            payload["thinking"] = {"type": "enabled", "budget_tokens": 5000 if complex_task else 2000}
        
        resp = requests.post(
            f"{BASE_URL}/v1/chat/completions",
            headers=HEADERS,
            json=payload,
            timeout=300 if complex_task else 120,
            stream=True
        )
        
        if resp.status_code != 200:
            result["error"] = f"HTTP {resp.status_code}: {resp.text[:500]}"
            return result
        
        content_parts = []
        reasoning_parts = []
        
        for line in resp.iter_lines():
            if line:
                line_str = line.decode('utf-8')
                if line_str.startswith("data: "):
                    data_str = line_str[6:]
                    if data_str.strip() == "[DONE]":
                        break
                    try:
                        data = json.loads(data_str)
                        result["chunks"] += 1
                        
                        choices = data.get("choices", [])
                        if not choices:
                            continue
                        choice = choices[0]
                        delta = choice.get("delta", {})
                        
                        # 检查 reasoning_content (Claude/OpenAI格式)
                        if "reasoning_content" in delta and delta["reasoning_content"]:
                            reasoning_parts.append(delta["reasoning_content"])
                            result["has_reasoning"] = True
                        
                        # 检查 thinking (Gemini格式)
                        if "thinking" in delta and delta["thinking"]:
                            reasoning_parts.append(delta["thinking"])
                            result["has_reasoning"] = True
                        
                        # 常规内容
                        if "content" in delta and delta["content"]:
                            content_parts.append(delta["content"])
                            
                    except json.JSONDecodeError as e:
                        pass
                    except Exception as e:
                        result["error"] = f"Parse error: {e}"
        
        result["reasoning_content"] = "".join(reasoning_parts)
        result["content"] = "".join(content_parts)
        result["success"] = result["chunks"] > 0 and (len(result["content"]) > 0 or len(result["reasoning_content"]) > 0)
        
    except requests.exceptions.Timeout:
        result["error"] = "Request timeout"
    except Exception as e:
        result["error"] = str(e)
    
    return result

def run_full_test():
    """运行完整测试"""
    print("\n" + "="*60)
    print("   CLIProxyAPI 全面测试")
    print("="*60)
    print(f"目标地址: {BASE_URL}")
    print(f"API Key: {API_KEY[:10]}...")
    
    # 1. 获取模型列表
    models = get_models()
    if not models:
        print("\n❌ 无法获取模型列表，测试终止")
        return
    
    # 2. 基础可用性测试
    print_separator("基础可用性测试")
    available_models = []
    for model in models:
        success, detail = test_model_basic(model)
        print_result(f"模型: {model}", success, detail)
        if success:
            available_models.append(model)
    
    print(f"\n可用模型: {len(available_models)}/{len(models)}")
    
    if not available_models:
        print("\n❌ 没有可用的模型，测试终止")
        return
    
    # 3. 流式输出测试
    print_separator("流式输出测试")
    streaming_results = {}
    for model in available_models:
        result = test_streaming(model)
        streaming_results[model] = result
        detail = f"chunks={result['chunks']}, content_len={len(result['content'])}"
        if result["error"]:
            detail = f"error: {result['error']}"
        print_result(f"模型: {model}", result["success"], detail)
    
    # 4. Thinking 模式测试 (简单任务)
    print_separator("Thinking 模式测试 (简单任务)")
    thinking_results = {}
    for model in available_models:
        result = test_thinking_mode(model, complex_task=False)
        thinking_results[model] = result
        detail = f"reasoning={result['has_reasoning']}, chunks={result['chunks']}"
        if result["error"]:
            detail = f"error: {result['error']}"
        print_result(f"模型: {model}", result["success"], detail)
    
    # 5. Thinking 模式测试 (复杂任务) - 只测试支持 thinking 的模型
    print_separator("Thinking 模式测试 (复杂任务)")
    complex_thinking_results = {}
    
    # 选择前3个可用模型进行复杂任务测试
    test_models = available_models[:3]
    print(f"测试模型 (取前3个): {test_models}\n")
    
    for model in test_models:
        print(f"⏳ 正在测试 {model} (复杂任务，可能需要较长时间)...")
        result = test_thinking_mode(model, complex_task=True)
        complex_thinking_results[model] = result
        
        if result["success"]:
            detail = f"reasoning={result['has_reasoning']}, reasoning_len={len(result['reasoning_content'])}, content_len={len(result['content'])}"
        else:
            detail = f"error: {result['error']}" if result["error"] else "Unknown error"
        
        print_result(f"模型: {model}", result["success"], detail)
        
        # 如果有 reasoning 内容，打印前500字符
        if result["has_reasoning"] and result["reasoning_content"]:
            print(f"\n       📝 Reasoning 内容预览 (前500字符):")
            print(f"       {result['reasoning_content'][:500]}...")
    
    # 6. 总结报告
    print_separator("测试总结报告")
    
    print(f"📊 模型总数: {len(models)}")
    print(f"✅ 可用模型: {len(available_models)}")
    print(f"❌ 不可用模型: {len(models) - len(available_models)}")
    
    print(f"\n📊 流式输出测试:")
    streaming_pass = sum(1 for r in streaming_results.values() if r["success"])
    print(f"   通过: {streaming_pass}/{len(streaming_results)}")
    
    print(f"\n📊 Thinking 模式测试 (简单):")
    thinking_pass = sum(1 for r in thinking_results.values() if r["success"])
    thinking_with_reasoning = sum(1 for r in thinking_results.values() if r["has_reasoning"])
    print(f"   通过: {thinking_pass}/{len(thinking_results)}")
    print(f"   包含推理内容: {thinking_with_reasoning}/{len(thinking_results)}")
    
    print(f"\n📊 Thinking 模式测试 (复杂):")
    complex_pass = sum(1 for r in complex_thinking_results.values() if r["success"])
    complex_with_reasoning = sum(1 for r in complex_thinking_results.values() if r["has_reasoning"])
    print(f"   通过: {complex_pass}/{len(complex_thinking_results)}")
    print(f"   包含推理内容: {complex_with_reasoning}/{len(complex_thinking_results)}")
    
    # 列出所有错误
    print(f"\n📋 错误详情:")
    has_errors = False
    
    for model, result in streaming_results.items():
        if result["error"]:
            has_errors = True
            print(f"   [流式] {model}: {result['error'][:100]}")
    
    for model, result in thinking_results.items():
        if result["error"]:
            has_errors = True
            print(f"   [Thinking简单] {model}: {result['error'][:100]}")
    
    for model, result in complex_thinking_results.items():
        if result["error"]:
            has_errors = True
            print(f"   [Thinking复杂] {model}: {result['error'][:100]}")
    
    if not has_errors:
        print("   无错误")
    
    print("\n" + "="*60)
    print("   测试完成")
    print("="*60 + "\n")

def test_single_model_basic(model: str):
    """单独测试一个模型的基础功能"""
    print_separator(f"基础测试: {model}")
    success, detail = test_model_basic(model)
    print_result(f"模型: {model}", success, detail)
    return success

def test_single_model_streaming(model: str):
    """单独测试一个模型的流式输出"""
    print_separator(f"流式测试: {model}")
    result = test_streaming(model)
    detail = f"chunks={result['chunks']}, content_len={len(result['content'])}"
    if result["error"]:
        detail = f"error: {result['error']}"
    print_result(f"模型: {model}", result["success"], detail)
    if result["content"]:
        print(f"\n内容: {result['content'][:300]}")
    return result

def test_single_model_thinking(model: str, complex_task: bool = False):
    """单独测试一个模型的thinking模式"""
    task_type = "复杂" if complex_task else "简单"
    print_separator(f"Thinking测试({task_type}): {model}")
    result = test_thinking_mode(model, complex_task=complex_task)
    detail = f"reasoning={result['has_reasoning']}, chunks={result['chunks']}"
    if result["error"]:
        detail = f"error: {result['error']}"
    print_result(f"模型: {model}", result["success"], detail)
    if result["reasoning_content"]:
        print(f"\nReasoning预览: {result['reasoning_content'][:500]}")
    if result["content"]:
        print(f"\n内容预览: {result['content'][:500]}")
    return result

def print_usage():
    print("""
用法: python test_api.py <command> [options]

命令:
  models              - 获取模型列表
  basic <model>       - 测试单个模型基础功能
  stream <model>      - 测试单个模型流式输出
  thinking <model>    - 测试单个模型thinking模式(简单任务)
  thinking-complex <model> - 测试单个模型thinking模式(复杂任务)
  all                 - 运行完整测试(原有功能)

示例:
  python test_api.py models
  python test_api.py basic claude-sonnet
  python test_api.py stream claude-sonnet
  python test_api.py thinking claude-sonnet
""")

if __name__ == "__main__":
    import sys
    
    if len(sys.argv) < 2:
        print_usage()
        sys.exit(0)
    
    cmd = sys.argv[1].lower()
    
    if cmd == "models":
        get_models()
    elif cmd == "basic" and len(sys.argv) >= 3:
        test_single_model_basic(sys.argv[2])
    elif cmd == "stream" and len(sys.argv) >= 3:
        test_single_model_streaming(sys.argv[2])
    elif cmd == "thinking" and len(sys.argv) >= 3:
        test_single_model_thinking(sys.argv[2], complex_task=False)
    elif cmd == "thinking-complex" and len(sys.argv) >= 3:
        test_single_model_thinking(sys.argv[2], complex_task=True)
    elif cmd == "all":
        run_full_test()
    else:
        print_usage()
