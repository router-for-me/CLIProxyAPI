package helps

import (
	"errors"
	"fmt"
	"time"

	"github.com/dop251/goja"
	log "github.com/sirupsen/logrus"
)

// js_engine 结构体包装了 Goja 虚拟机运行时，在类内部实现了 console.log 的自动注入，为外部提供高内聚且易用的交互接口。
type js_engine struct {
	vm *goja.Runtime
}

// new_js_engine 创建并初始化一个独立的 js_engine 实例，类内部会自动完成控制台输出的配置，对外部使用者完全屏蔽底层实现细节。
func new_js_engine() *js_engine {
	engine := &js_engine{
		vm: goja.New(),
	}
	engine.init_console()
	return engine
}

// init_console 是引擎类的内部私有方法，用于将 JS 中的 console.log 绑定到 Go 的标准控制台输出。
func (engine *js_engine) init_console() {
	console := engine.vm.NewObject()
	console_log_wrapper := func(call goja.FunctionCall) goja.Value {
		args := make([]interface{}, len(call.Arguments))
		for i, arg := range call.Arguments {
			args[i] = arg.Export()
		}
		// 根据全局日志规则要求，使用中文输出日志
		log.Infof("JS 控制台日志: %v", args...)
		return goja.Undefined()
	}
	_ = console.Set("log", console_log_wrapper)
	_ = engine.vm.Set("console", console)
}

// run_program 加载并运行已预编译的 goja.Program 程序。
func (engine *js_engine) run_program(program *goja.Program) error {
	if program == nil {
		return errors.New("程序对象为空。")
	}
	_, err := engine.vm.RunProgram(program)
	if err != nil {
		return fmt.Errorf("运行 JS 预编译程序失败: %w", err)
	}
	return nil
}

// ErrFunctionNotFound 表示调用的 JS 函数未找到或未定义的哨兵错误。
var ErrFunctionNotFound = errors.New("函数未找到")

// call_function 用来在 Go 中同步调用 JS 的全局函数，并传参、获取返回值，同时支持执行超时保护。
func (engine *js_engine) call_function(name string, timeout time.Duration, args ...interface{}) (goja.Value, error) {
	js_val := engine.vm.Get(name)
	if js_val == nil || goja.IsUndefined(js_val) {
		return nil, fmt.Errorf("%w: 函数 '%s' 不存在", ErrFunctionNotFound, name)
	}
	js_func, ok := goja.AssertFunction(js_val)
	if !ok {
		return nil, fmt.Errorf("函数 '%s' 无效", name)
	}

	js_args := make([]goja.Value, len(args))
	for i, arg := range args {
		js_args[i] = engine.vm.ToValue(arg)
	}

	// 启动超时熔断保护，防止执行 JS 时发生无限死循环
	timer := time.AfterFunc(timeout, func() {
		engine.vm.Interrupt(errors.New("javascript 执行超时。"))
	})
	defer timer.Stop()

	result, err := js_func(goja.Undefined(), js_args...)
	if err != nil {
		return nil, err
	}

	return result, nil
}
