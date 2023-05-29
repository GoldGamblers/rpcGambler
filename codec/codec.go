package codec

import (
	"io"
)

// RPC 请求示例
//err = client.Call("Arith.Multiply", args, &reply)
// 请求：服务名 Arith，方法名 Multiply，参数 args
// 响应：误 error，返回值 reply

// Header 将请求和响应的参数和返回值抽象为 body，剩余部分放在 Header 中
type Header struct {
	ServiceMethod string // 服务名和方法名，通常和结构体、方法相映射
	Error         string // 错误信息
	Seq           uint64 // 请求的序号，或者是某个请求的 ID，目的是区分不同的请求
}

// Codec 抽象出对消息体编码的接口，便于实现不同的 Codec 实例
type Codec interface {
	io.Closer                         // 关闭 IO
	ReadHeader(*Header) error         // 用于读取Header
	ReadBody(interface{}) error       // 用于读取 Body
	Write(*Header, interface{}) error // 用于编码 请求头 和 body
}

// NewCodecFunc 定义一个函数类型
type NewCodecFunc func(io.ReadWriteCloser) Codec

type Type string

const (
	GobType  Type = "application/gob"  // 只用到了这个
	JsonType Type = "application/json" // 没有用到这个
)

// NewCodecFuncMap 建立 Type 和 构造函数的映射关系。也就是 Gob和JSON
var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
