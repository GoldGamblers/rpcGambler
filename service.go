package gamblerRPC

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

// 通过反射实现结构体和服务的映射

// methodType 一个 methodType 实例包含一个方法完整的信息
type methodType struct {
	method    reflect.Method // 方法本身
	ArgType   reflect.Type   // 传入的参数
	ReplyType reflect.Type   // 返回值，但是这个也是在方法的参数中写的，因为前面讲了 RPC 的调用方式
	numCalls  uint64         // 统计方法调用次数时用到
}

// NumCalls 用于统计方法调用次数
func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls)
}

// newArgv 创建 ArgType 类型的实例
func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	// argv 可能是指针类型也可能是值类型
	if m.ArgType.Kind() == reflect.Pointer {
		argv = reflect.New(m.ArgType.Elem())
	} else {
		argv = reflect.New(m.ArgType).Elem()
	}
	return argv
}

// newReply 创建 ReplyType 类型的实例
func (m *methodType) newReplyv() reflect.Value {
	// reply 必须是一个指针类型
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}
	return replyv
}

// service 定义一个结构体的信息
type service struct {
	name   string                 // 映射结构体的名称
	typ    reflect.Type           // 结构体的类型
	rcvr   reflect.Value          // 结构体的实例本身，rcvr在调用时作为第0个参数
	method map[string]*methodType // 存储该结构体所有符合条件的方法
}

// newService rcvr 是需要映射为服务的结构体实例
func newService(rcvr interface{}) *service {
	s := new(service)
	// 结构体实例本身 是一个地址
	s.rcvr = reflect.ValueOf(rcvr)
	// 结构体实例的名字 Foo
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	// 结构体实例的类型 *gamblerRPC.Foo
	s.typ = reflect.TypeOf(rcvr)
	//fmt.Printf("s.name = %v, s.typ = %v\n", s.name, s.typ)
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	// 过滤符合条件的方法
	s.registerMethods()
	return s
}

// registerMethods 过滤符合条件的方法并保存
func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i++ {
		// 拿到该结构体的每个方法
		method := s.typ.Method(i)
		mType := method.Type
		// 这里只是针对 Sum 方法设置的过滤条件
		// Sum 方法是两个参数值，加上自身是三个，第0个也就是自身，类似于 python 的 self，java 中的 this）
		// 返回值有且只有 1 个，类型为 error
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		// 返回值不是 error 类型
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		// 检查 argType 是否是可导出的
		argType, replyType := mType.In(1), mType.In(2)
		if !isExportedOrBuiltinType(argType) {
			continue
		}
		// 保存方法本身和参数及返回值
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

// isExportedOrBuiltinType
func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

// call 能够通过反射值调用方法
func (s *service) call(m *methodType, argv, reply reflect.Value) error {
	// 用于自动将增量添加到 *addr，也就是 方法的 numCalls 参数
	atomic.AddUint64(&m.numCalls, 1)
	// 定义一个 以接收者为第一个参数的函数
	f := m.method.Func
	// 调用并得到返回值
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, reply})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
