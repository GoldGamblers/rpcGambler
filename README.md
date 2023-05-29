# rpcGambler 框架设计实现

RPC(Remote Procedure Call，远程过程调用)是一种计算机通信协议，允许调用不同进程空间的程序。RPC 的客户端和服务器可以在一台机器上，也可以在不同的机器上。程序员使用时，就像调用本地程序一样，无需关注内部的实现细节。不同的应用程序之间的通信方式有很多，比如浏览器和服务器之间广泛使用的基于 HTTP 协议的 Restful API。与 RPC 相比，Restful API 有相对统一的标准，因而更通用，兼容性更好，支持不同的语言。HTTP 协议是基于文本的，一般具备更好的可读性。但是缺点也很明显：

    Restful 接口需要额外的定义，无论是客户端还是服务端，都需要额外的代码来处理，而 RPC 调用则更接近于直接调用。
    基于 HTTP 协议的 Restful 报文冗余，承载了过多的无效信息，而 RPC 通常使用自定义的协议格式，减少冗余报文。
    RPC 可以采用更高效的序列化协议，将文本转为二进制传输，获得更高的性能。
    因为 RPC 的灵活性，所以更容易扩展和集成诸如注册中心、负载均衡等功能。

## RPC 框架要解决的问题
如果要在两个应用程序之间进行通信，有很多问题需要解决。

首先要确定传输的协议是什么。如果两个程序在不同的物理机上，通常会使用 TCP 或者 HTTP 协议；如果两个程序在同一个物理机上，那么可以使用 Unix Socket 进行通信。

其次要确定报文的编码和解码方式。例如常用的 JSON 或者 XML，如果对于报文的传输速度和体积要求较高，还可以使用 Protobuf 进行编码和解码。

还有很多其他的细节需要处理，例如连接超时如何处理、如何做到异步请求和并发。如果提供服务的是多个实例，那么客户端本身其实并不关心这些实例的地址，只关心能否拿到自己想要的结果，因此还可能需要注册中心和负载均衡机制。

如果服务端是不同的团队提供的，那么这些服务的编码方式可能是不同的，那么就需要服务提供方就需要在实现一套消息编码解码等等一套流程。因此 RPC 存在的基本意义便是处理业务之外的公共能力。

本框架的实现参考了 Go 的官方库 net / rpc，并且新增了注册中心、负载均衡、超时处理、服务发现以及协议交换功能。


## 一、定义消息的编码和解码方式
RPC 的调用过程如下：有三个参数，第一个参数代表请求的服务名和方法名、args 是传入的参数、reply 用于接收参数。

```go
err = client.Call("serviceName.methodName", args, &reply)
```
现在将请求参数和响应参数抽象为一个 body 结构，其他的信息抽象为 Header 结构，到此我们就定义了一个我们自己的协议结构。

```go
type Header struct {
    ServiceMethod string // 服务名和方法名，通常和结构体、方法相映射
    Error         string // 错误信息
    Seq           uint64 // 请求的序号，或者是某个请求的 ID，目的是区分不同的请求
}
```

然后要抽象一个编码解码的接口，用于实现不同的编码和解码实例，例如 Gob 或者 JSON。这些功能是每一种编码解码实例所必须具备或者说通用的功能。

```go
type Codec interface {
    io.Closer                         // 关闭 IO
    ReadHeader(*Header) error         // 用于读取Header
    ReadBody(interface{}) error       // 用于读取 Body
    Write(*Header, interface{}) error // 用于写入缓冲区
}
```

接口有了接下来就要提供一个 Codec 的构造函数。在编写构造函数之前需要先定义一个构造函数类型，这样可以便于用户通过作为 key 的 Type 字段来获得不同编解码方式的构造函数。因为我们会将每一种编解码方式和其对应的构造函数保存在 map 中。

```go
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
    // 这里只实现 Gob 编码方式
    NewCodecFuncMap[GobType] = NewGobCodec
}
```

## 二、实现 Gob 编解码
Gob 是 Go 自己的二进制形式序列化和反序列化程序数据的格式。首先将编解码需要的成员抽象为一个结构体。并提供一个构造函数。

```go
type GobCodec struct {
    conn io.WriteCloser // 由构建函数传入，通常是通过 TCP 或者 Unix 建立 socket 时得到的链接实例
    buf  *bufio.Writer  // 防止阻塞而创建的带缓冲的 Writer
    dec  *gob.Decoder
    enc  *gob.Encoder
}
```

然后要实现编解码器应该实现的接口。这里有个小技巧可以检查 GobCodec 对象是否全部实现了 Codec 定义的接口。只有 Write() 方法需要特殊处理，Read 相关方法直接调用 GobCodec 的解码器即可：c.dec.Decode()

```go
var _ Codec = (*GobCodec)(nil)
```

下面来看一下 Write() 方法的实现。Write() 的逻辑很简单，其实就是调用 Encode() 对 Header 和 Body 进行编码。编码完整之后调用 c.buf.Flush() 将编码好的数据写入到缓冲区。

```go
func (c *GobCodec) Write(h *Header, body interface{}) (err error) {
    //TODO implement me
    defer func() {
        // 刷新缓冲区,只要用到缓冲区,就要记得刷新
        _ = c.buf.Flush()
        if err != nil {
            _ = c.Close()
        }
    }()
    // 先执行，最后执行 defer 函数
    if err := c.enc.Encode(h); err != nil {
        log.Println("rpc codec: gob error encoding header:", err)
        return err
    }
    if err := c.enc.Encode(body); err != nil {
        log.Println("rpc codec: gob error encoding body:", err)
        return err
    }
    return nil
}
```

## 三、实现一个基础的服务端框架
### 1、客户端和服务端的协商信息设计
客户端和服务端通信时，有一些内容是需要协商的，在协商之后才正式开始进行数据的传输等。以 HTTP 协议为例，在 Header 部分会规定一些解析的方式，例如如何解析 Body 部分的数据，采用什么协议等等。对于 RPC 协议来说这部分是需要自己设计的，协商的信息一般来说要设计固定的字节来传输以提高传输效率，但是为了实现简单，客户端采用 JSON 编码协商信息，但是之后的 Header 和 Body 会采用协商好的方式进行编解码。这样一例埃，服务端会先使用 JSON 解码这部分协商信息，然后再使用协商好的编解码方式进行后续的通信。也就是说整体上是这样的格式：

```
|        协商信息         |     Header     |     Body    |
| <-  固定 JSON 编码  ->  | <- 编码方式由 CodeType 决定 ->|
```

在一次连接中，协商信息固定在报文的最开始，Header 和 Body 可以有多个，即报文可能是这样的。

    | 协商信息 | Header1 | Body1 | Header2 | Body2 | ...

所以要定义一个结构来承载协商信息，目前需要的协商信息只有编解码方式,并且设置一个默认的协商信息，采用的是 Gob 编解码。
```go
type Option struct {
    MagicNumber int        // 用于标记 RPC 的请求
    CodecType   codec.Type // 客户端可能会选择不用的 Codec 去编码，这里只实现的 gob
}

// 默认协商信息
var DefaultOption = &Option{
    MagicNumber: MagicNumber,
    CodecType:   codec.GobType,
}
```

我们将 Header 和 Body 抽象为 request 结构。
```go
type request struct {
    h            *codec.Header // 请求的 Header 部分，包括服务名、方法名等
    argv, replyv reflect.Value // 请求的 参数 and 回复值，是 body 部分
}
```

### 2、服务端实现连接和消息解码
首先定义服务端并实现服务端的实例，并给 serve 实例提供一个建立连接的方法。在创建服务端实例的时候提供一个默认的服务端实例，作用是给默认的建立连接函数提供一个默认的服务端实例。

```go
var DefaultServer = NewServer()
func Accept(lis net.Listener) {
    DefaultServer.Accept(lis)
}
```
然后实现对协商信息 Option 的解析方法 ServerConn。之前提到为了实现方便协商信息采用的是 JSON 格式的编码解码。所以在服务端要将 Option 解码到 opt 对象中保存。

```go
var opt Option
if err := json.NewDecoder(conn).Decode(&opt); err != nil {
    log.Println("rpc server: options decode error: ", err)
    return
}
```
解析完成后就可以从 opt 对象中拿到对应的编解码方式的构造函数，并将 conn 作为参数传递给构造函数创建指定的编解码器。最后将这个创建的编解码器传递给 serveCodec()

```go
f := codec.NewCodecFuncMap[opt.CodecType]
if f == nil {
    log.Printf("rpc server: invalid codec type %s", opt.CodecType)
    return
}
server.serveCodec(f(conn))
```

在 serveCodec() 中，将传入的编解码器作为 readRequest() 方法的参数来解析请求得到包含 header 和 body 的请求 req 对象，解析完成后将 req 传入 handleRequest() 函数处理请求。这里注意要使用两个锁机制，sync.Mutex 用于保证一个响应能够完整发送，waitGroup 用于保证请求处理的并发安全。
```go
func (server *Server) serveCodec(cc codec.Codec) {
    // 用于并发控制
    // 确定发送一个完整的响应
    sending := new(sync.Mutex)
    // 等待所有的请求都被处理
    wg := new(sync.WaitGroup)
    for {
        // 读取请求
        req, err := server.readRequest(cc)
        // 读取失败
        if err != nil {
            if req == nil {
                // 无法实现 recover 所以直接跳出循环关闭连接
                break
            }
            req.h.Error = err.Error()
            // 回复请求
            server.sendResponse(cc, req.h, invalidRequest, sending)
            continue
        }
        // 读取成功后处理请求
        wg.Add(1)
        go server.handleRequest(cc, req, sending, wg)
    }
    wg.Wait()
    _ = cc.Close()
}
```

所以在这个过程中，我们要实现对请求的解析和对请求的处理两个函数。其实就是调用编解码器实现的 ReadHeader() 方法和 ReadBody() 方法解析得到 Header 和 Body 并保存到 request 对象中，并进行后续的处理。

readRequest() 方法将解析得到的 Header 和 Body 保存到 request 对象中。
```go
func (server *Server) readRequest(cc codec.Codec) (*request, error) 
```
handleRequest() 方法我们目前仅实现将请求返回的序号。构造好返回值 req.replyv 后调用 sendResponse() 方法将响应返回。sendResponse() 方法是对 编解码器实现的 Write() 方法的封装。
```go
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup)

func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex)
```

目前服务端实现的功能是接收请求并返回请求的序号。至此简易的服务端框架已经搭建完毕，能够建立连接、读取请求、处理请求、回复请求。后续会在这个服务端的框架上进行迭代。而且也已经实现了协议交换，支持客户端选择不同的编解码协议。

下面实现一个简单的客户端来测试一下。首先实现一个启动服务的方法 startServer(),参数是一个管道，成功建立 TCP 连接后将监听地址写到管道中。

```go
func startServer(addr chan string) {
    l, err := net.Listen("tcp", ":0")
    if err != nil {
        log.Fatal("network error:", err)
    }
    log.Println("start rpc server on", l.Addr())
    addr <- l.Addr().String()
    gamblerRPC.Accept(l)
}
```

在 main 函数中，定义一个管道作为启动服务函数的参数。启动服务后使用 net.Dial("tcp", <-addr) 建立连接并把连接 conn 作为参数传递给 JSON 编码器构造函数，用 JSON 编码器来编码默认的协商信息。然后客户端创建 Gob 编解码方式的编解码器。接下来就要构造请求的 Header 和 Body 部分。构建好后调用 编解码器的 Write() 方法向服务端发送消息。最后等待读取返回结果即可。

```go
func main() {
    addr := make(chan string)
    go startServer(addr)
    // 从 addr 管道中读数据
    conn, _ := net.Dial("tcp", <-addr)
    defer func() { _ = conn.Close() }()
    time.Sleep(time.Second)
    // 发送 Options
    _ = json.NewEncoder(conn).Encode(gamblerRPC.DefaultOption)
    // gob 处理方式
    cc := codec.NewGobCodec(conn)
    // 发送请求并且收到回复
    for i := 0; i < 5; i++ {
        // 发送5个请求，i代表5个请求的 ID
        h := &codec.Header{
            ServiceMethod: "Foo.Sum",
            Seq:           uint64(i),
        }
        _ = cc.Write(h, fmt.Sprintf("gamblerRPC req %d", h.Seq))
        _ = cc.ReadHeader(h)
        var reply string
        _ = cc.ReadBody(&reply)
        log.Println("reply:", reply)
    }
}
```

## 四、客户端的设计
### 1、定义请求信息的载体
客户端要支持异步和并发。客户端最重要的功能就是发起请求并接收响应。所以首先定义一个结构 Call 用来承载一次请求需要的信息。
```go
type Call struct {
    Seq           uint64      // 用于区分请求，相当于请求的 ID
    ServiceMethod string      // 用于保存请求的服务名和方法名， format "<service>.<method>"
    Args          interface{} // 用于保存请求的参数
    Reply         interface{} // 用于保存响应的数据
    Error         error       // 发生错误时返回错误信息
    Done          chan *Call  // 支持异步调用，当调用结束时使用 call.done() 通知调用方
}
```
接着给 Call() 定义一个 Done 方法，用于调用结束的时候把这个 call 写入到 chan 中。
```go
func (call *Call) done() {
    // Done 是一个 *Call 类型的 chan，这里是调用结束时把 call 写入到 chan 中
    call.Done <- call
}
```
### 2、定义客户端结构
```go
type Client struct {
    cc       codec.Codec      // 不同的 codec 实例，比如针对gob的消息编码器和解码器或者是针对其他类型的
    opt      *Option          // 确定消息的编码方式,承载的是协商信息
    sending  sync.Mutex       // 保证请求的有序发送
    header   codec.Header     // 每个请求的消息头，header 只有在请求发送时才需要，而请求发送是互斥的，因此每个客户端只需要一个
    mu       sync.Mutex       // 用于每个客户端的并发控制
    seq      uint64           // 区分不同的请求，相当于请求的 ID
    pending  map[uint64]*Call // 存储没有处理完的请求，键是请求的编号，值是请求
    closing  bool             // closing 为 true 是用户主动关闭的
    shutdown bool             // shutdown 为 true 一般是有错误发生
}
```

客户端要实现 io.Closer 接口的 Close 方法来关闭客户端连接，并提供一个基础的判断客户端是否存活的方法，这个方法的逻辑就是判断 closing 和 shutdown 字段的值。

### 3、实现与发起请求、接收请求相关的基础方法
包括请求注册、请求移除、请求中断。

请求注册的功能是将发起的请求和对应的请求 ID 建立映射关系保存到 pending 中表示请求待处理。请求的 ID 是通过客户端的 seq 得到的，每次添加完一个请求后要更新 client.seq，用于标识不同的请求。
```go
func (client *Client) registerCall(call *Call) (uint64, error)
```
请求移除的功能是移除指定 ID 的请求，表示请求已经处理完毕，参数是请求 ID。
```go
func (client *Client) removeCall(seq uint64) *Call
```
请求中断的功能是当服务器或者客户端调用发生错误时，设置 shutdown 字段并将错误信息通知给 pending 中所有待执行请求,并调用 Done 结束所有的请求。
```go
func (client *Client) terminateCalls(err error)
```

### 4、实现接收请求的功能
接收功能的逻辑是这样的：首先通过 Header 拿到请求的 ID，然后调用移除请求的方法将这个请求从待处理请求 map 中移除，这个方法会返回这个移除的请求。

```go
// 读取 Header
err := client.cc.ReadHeader(&h)
call := client.removeCall(h.Seq)
```
此时 Call 有三种情况，我们用 switch 结构处理。如果不是这三种情况，那就调用 terminateCalls() 方法表示出现了未知错误。
```go
switch {
    // call 不存在，可能是请求没有发送完整，或者因为其他原因被取消，但是服务端仍旧处理了
    case call == nil:
        err = client.cc.ReadBody(nil)
    // call 存在，但服务端处理出错，即 h.Error 不为空。
    case h.Error != "":
        call.Error = fmt.Errorf(h.Error)
        err = client.cc.ReadBody(nil)
        call.done()
    // call 存在，服务端处理正常，那么需要从 body 中读取 Reply 的值
    default:
        err = client.cc.ReadBody(call.Reply)
        if err != nil {
            call.Error = errors.New("reading body " + err.Error())
        }
        call.done()
}
```
### 5、实现发送请求的功能
发送请求需要三个方法来实现，分别是 GenerateCall、send、Call。

GenerateCall 负责构建一个 call 请求并调用 send 方法发送。
```go
func (client *Client) GenerateCall(serviceMethod string, args interface{}, reply interface{}, done chan *Call) *Call 
```

Call 方法是对 GenerateCall 方法的封装，用于暴露给用户进行调用。最后的 .Done 操作是为了等待响应返回，利用 channel 进行阻塞。这是一个同步接口。
```go
func (client *Client) Call(serviceMethod string, args interface{}, reply interface{}) error {
    call := <-client.GenerateCall(serviceMethod, args, reply, make(chan *Call, 1)).Done
    return call.Error
}
```

send 方法主要实现的是根据传入的 Call 请求调用 registerCall 方法注册请求并返回请求的 ID。然后构建客户端的 Header 部分，这部分内容是通过 call 的 Header 构建的。
最后使用协商好的编码器实现的 Write() 方法进行发送。发送请求后就将请求移除，表示请求已经被处理。处理成功会调用 Done 方法。关键代码如下：

```go
// 注册一个请求
seq, err := client.registerCall(call)
// 准备请求头
client.header.ServiceMethod = call.ServiceMethod
client.header.Seq = call.Seq
client.header.Error = ""
// 编码并且发送请求
if err := client.cc.Write(&client.header, call.Args); err != nil {
    call := client.removeCall(seq)
    if call != nil {
        // call 可能为 nil，通常表示 Write 部分失败
        // 客户已收到响应并处理
        call.Error = err
        call.done()
    }
}
```

### 6、创建客户端实例并建立连接
创建客户端为什么放在这里实现？是因为创建客户端时要协商后续消息的编码方式，所以先实现接收和发送再来实现客户端的创建和连接的建立。

创建客户端首先需要一个 newClient 方法，创建客户端时需要传入解析好的 协商信息 和 连接。客户端根据这个协商信息在 NewCodecFuncMap 中拿到对应的编解码器的构造函数。客户端要把这个协商信息发送给服务端。最后将 conn 连接传递给构造函数，将这个编码器实例传递给 newClientCodec 方法。
```go
func NewClient(conn net.Conn, opt *Option) (*Client, error) 
```

这里将创建客户端并接受响应的逻辑抽象到 newClientCodec 方法中。其实这个方法中才真正包含了创建客户端实例的逻辑。
```go
func newClientCodec(cc codec.Codec, opt *Option) *Client {
    client := &Client{
        seq:     1,
        cc:      cc,
        opt:     opt,
        pending: make(map[uint64]*Call),
    }
    // 协商好消息的编解码方式之后，再创建一个子协程调用 receive() 接收响应
    go client.receive()
    return client
}
```

完成以上功能之后我们需要一个暴露给用户的接口并在这个接口中完成对 Option 协商信息的解析、与指定地址建立连接、将连接和协商信息传递给 NewClient 方法创建客户端。在解析 Option 时如果传入的是空值则采用默认的 Option。

```go
func Dial(network, address string, opts ...*Option) (client *Client, err error)
```
建立连接的方式就是直接调用 net 包中的 Dial。注意使用 defer 关闭连接。
```go
conn, err := net.Dial(network, address)
```
最后 return 一个客户端。
```go
return NewClient(conn, opt)
```

### 7、测试
测试的话和之前类似，启动 PRC 服务的逻辑没有变化，其他部分只有两处变化。

第一处是在建立连接时采用了封装的 Dial 方法。
```go
conn, _ := net.Dial("tcp", <-addr)
↓ ↓ ↓ ↓ ↓
client, _ := gamblerRPC.Dial("tcp", <-addr)
```
第二处是发起请求时，不需要自己构建 Header 并使用 Write() 发送了，而是通过直接调用 Call 方法完成请求信息的构建并进行发送。

```go
for i := 0; i < 5; i++ {
    // 发送5个请求，i代表5个请求的 ID
    h := &codec.Header{
        ServiceMethod: "Foo.Sum",
        Seq:           uint64(i),
    }
    _ = cc.Write(h, fmt.Sprintf("gamblerRPC req %d", h.Seq))
    _ = cc.ReadHeader(h)
    var reply string
    _ = cc.ReadBody(&reply)
    log.Println("reply:", reply)
}

↓ ↓ ↓ ↓ ↓ 

var wg sync.WaitGroup
for i := 0; i < 5; i++ {
    wg.Add(1)
    go func(i int) {
        defer wg.Done()
        args := fmt.Sprintf("gamblerRPC req %d", i)
        var reply string
        if err := client.Call("serviceName.methodName", args, &reply); err != nil {
            log.Fatal("call serviceName.methodName error:", err)
        }
        log.Println("reply:", reply)
    }(i)
}
wg.Wait()
```
## 五、实现结构体和其方法的映射
之前设计的服务端只能提供简单的功能，返回请求的序号，这主要是为了搭建一个客户端和服务端的雏形。对于 Golang 来说现在要来实现将结构体映射为服务的功能。在 net/rpc 中一个函数需要能够被远程调用，需要满足如下五个条件：
```
方法所属类型是导出的
方法是导出的
两个入参，均为导出或内置类型
第二个入参必须是一个指针
返回值为 error 类型
```
```go
func (t *T) MethodName(argType T1, replyType *T2) error
```
第一个参数是给远程服务的参数，第二个参数是从远程服务拿到的返回值。service对应的是结构体名，method 对应的是该结构体的方法名。例如 "T.MethodName" 表示 T 结构体的 MethodName 方法。但是服务名和方法非常多，我们不可能采用硬编码的方式去对每一个服务和方法做处理。所以我们需要一种通用的、自动化的方式来获取这些信息。这就用到了反射技术。反射使用举例：
```go
func main() {
    var wg sync.WaitGroup
    // typ = *sync.WaitGroup 指针对象
    typ := reflect.TypeOf(&wg)
    // NumMethod() 拿到方法的个数
    for i := 0; i < typ.NumMethod(); i++ {
        // 遍历每一个方法
        method := typ.Method(i)
        // 拿到参数
        argv := make([]string, 0, method.Type.NumIn())
        returns := make([]string, 0, method.Type.NumOut())
        // j 从 1 开始，保存这个方法的入参类型，第 0 个入参是 wg 自己。
        for j := 1; j < method.Type.NumIn(); j++ {
            argv = append(argv, method.Type.In(j).Name())
        }
        // 将这个方法的返回值类型保存到 returns 切片中
        for j := 0; j < method.Type.NumOut(); j++ {
            returns = append(returns, method.Type.Out(j).Name())
        }
        // typ.Elem() = sync.WaitGroup，包含包名
        // typ.Elem().Name() = WaitGroup 结构体名
        log.Printf("func (w *%s) %s(%s) %s",
            typ.Elem().Name(),
            method.Name,
            strings.Join(argv, ","),
            strings.Join(returns, ","))
    }
}
```
可以看到通过反射技术我们可以实现一个通用的方式来得到服务和方法的信息。有了这些信息，我们就可以实现服务的注册和调用了。

### 1、实现结构体和服务的映射
首先要定义一个结构保存方法的信息。并实现 newArgv 和 newReplyv 方法用于创建对应的reflect.Value 类型实例，这两个方法是在读取请求时调用的，作为入参。
```go
type methodType struct {
    method    reflect.Method // 方法本身
    ArgType   reflect.Type   // 传入的参数的类型
    ReplyType reflect.Type   // 返回值的类型，但是这个也是在方法的参数中写的，因为前面讲了 RPC 的调用方式
    numCalls  uint64         // 统计方法调用次数时用到
}
```
然后定义一个结构保存服务的信息，也就是结构体的信息。需要 rcvr 是因为在发起调用的时候，需要 rcvr 作为第 0 个参数。
```go
type service struct {
    name   string                 // 映射结构体的名称
    typ    reflect.Type           // 结构体的类型
    rcvr   reflect.Value          // 结构体的实例本身，rcvr在调用时作为第0个参数
    method map[string]*methodType // 存储该结构体所有符合条件的方法
}
```
提供构造函数，入参是任意的结构体,功能是将结构体映射为服务。service 结构的字段需要通过反射技术获取。

```go
func newService(rcvr interface{}) *service

// 使用反射
// 结构体实例的名字 Foo
s.name = reflect.Indirect(s.rcvr).Type().Name()
// 结构体实例的类型 *gamblerRPC.Foo
s.typ = reflect.TypeOf(rcvr)
```
在将结构体映射为服务的过程中还需要将该结构体所有的方法添加到服务的 map 中。
```go
func (s *service) registerMethods()
```
实现的逻辑是：首先拿到方法个数，再遍历每一个方法，拿到每一个方法的所有入参和返回值，最后保存映射关系。当然在这其中还会设置一些过滤条件，比如入参和返回值的个数、入参和返回值的类型等等。
```go
// 获取方法个数
s.typ.NumMethod()
// 获取方法及其类型
method := s.typ.Method(i)
mType := method.Type
// 保存方法本身和参数及返回值
s.method[method.Name] = &methodType{
    method:    method,
    ArgType:   argType,
    ReplyType: replyType,
}
```

最后提供一个 call 方法，能够通过反射值来调用对应的方法。其实是对 反射包中 call 方法的封装。
```go
func (s *service) call(m *methodType, argv, reply reflect.Value) error
```
这里的会用到 go 的反射包提供的函数，reflect.Method 有一个 Func 成员变量，通过这个变量可以获得这个方法。然后再调用 Call 方法将结构体自己、参数、返回值作为参数调用。
```go
f := m.method.Func
returnValues := f.Call([]reflect.Value{s.rcvr, argv, reply})
```

### 2、集成到服务端
目前已经实现了将结构体映射为服务，并通过反射值调用对应方法获取返回值的功能。服务端目前还需要实现三个功能。第一个是根据入参的类型将请求的 Body 部分反序列化。第二个是调用 call 方法获取返回值。第三个是将 reply 序列化成字节流构造响应。

在此之前要首先实现一个 服务注册 的方法，这个方法是对 newService 方法的封装。如果已经注册会返回注册失败的信息。然后给默认的服务端提供一个注册方法，便于直接调用。
```go
func (server *Server) Register(rcvr interface{}) error

func Register(rcvr interface{}) error { return DefaultServer.Register(rcvr) }
```
有了注册服务还要有发现服务的功能。逻辑写在 findService 方法中。
```go
func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error)
```
首先检查 . 的位置，因为服务名和方法名是用 . 来分割的，根据点的位置可以判断这是不是合法。然后分别拿到 serviceName 和 methodName。接着调用 map 的 Load 方法去查找 serviceName 对应的服务 svc。通过 methodName 在 svc 的 method 映射中找到对应的方法信息 mtype 对象。

接下来要实第一个功能。这个功能在 readRequest 方法中。首先要请求信息中新增两个成员变量，分别是 方法 和 服务。
```go
type request struct {
    h            *codec.Header // 请求的 Header
    argv, replyv reflect.Value // 请求的所有参数 and 回复值
    mtype        *methodType   // 请求的方法实例
    svc          *service      // 请求的结构体实例
}
```
在 readRequest 方法中，首先实现根据入参类型，将请求的 body 反序列化，反序列化的过程是通过 ReadBody() 方法实现的，这个方法是 编解码器实现的一个接口方法。同时实例化一个 reflect.Value 类型的返回值 保存在 request 结构中，用于后续保存方法的返回值。
```go
// 创建参数和返回值
req.argv = req.mtype.newArgv()
req.replyv = req.mtype.newReplyv()
// argv 必须是指针，读 body 需要指针
argvi := req.argv.Interface()
if req.argv.Type().Kind() != reflect.Pointer {
    argvi = req.argv.Addr().Interface()
}
err = cc.ReadBody(argvi)
```

在 handleRequest 中，调用 service 的 call 方法获取返回值，保存到 req.replyv 中。之后的过程就是调用 sendResponse 方法，在 sendResponse 中调用 编解码器实现的 Write 接口方法序列化返回值并写入到缓存中。
```go
err := req.svc.call(req.mtype, req.argv, req.replyv)
```
### 3、进行测试
在 main.go 中编写测试用例。
```go
type Foo int
type Args struct {
    Num1 int
    Num2 int
}
// Sum 可导出的方法, 第一个参数是函数的入参，第二个参数是返回的结果，必须是指针类型才能被调用者接收到
func (f Foo) Sum(args Args, reply *int) error {
    *reply = args.Num1 + args.Num2
    return nil
}
```
启动 RPC 服务时，需要调用服务注册方法将结构体映射为服务。其余的逻辑没有变化。
```go
// 注册 Foo
var foo Foo
if err := gamblerRPC.Register(&foo); err != nil {
    log.Fatal("register error:", err)
}
```
在 main 函数中的逻辑也没有什么变化，只是需要构建一下 入参，也就是 Args
```go
// 构建入参
args := &Args{
    Num1: i,
    Num2: i * i,
}
// 调用并接收返回值
var reply int
err := client.Call("Foo.Sum", args, &reply)
```

测试结果日志：
```text
rpc server: register Foo.Sum
start rpc server on [::]:61034
address:  [::]:61034
0 + 0 = 0
3 + 9 = 12
2 + 4 = 6
4 + 16 = 20
1 + 1 = 2
```

## 六、超时处理
超时处理也是 rpc 框架的基本功能。没有超时机制，很容易在客户端或者服务端出现因为网络等问题出现宕机情况，导致资源耗尽。从整个 rpc 的框架来看：

在客户端部分需要处理的超时场景：
```text
1、建立连接超时
2、发送请求写报文超时
3、等待服务端处理结果超时
4、读取服务端响应时超时
```

在服务端部分需要处理的超时场景：
```text
1、读取客户端请求报文时超时
2、发送响应报文时写报文超时
3、调用映射服务的方法时处理报文超时
```

本 rpc 框架设计超时处理机制的位置：
```text
1、客户端创建连接时的超时处理
2、客户端调用的整个过程导致的超时   Client.Call()
3、服务端处理报文时超时    Server.handleRequest
```

### 1、实现连接超时处理
超时的设定放在协商信息 Option 中,所以新增两个 time.Duration 类型的成员变量。
```go
type Option struct {
    MagicNumber       int           // 用于标记 RPC 的请求
    CodecType         codec.Type    // 客户端可能会选择不用的 Codec 去编码，这里只实现的 gob
    ConnectionTimeout time.Duration // 连接超时，0 代表不设限制
    HandleTimeout     time.Duration // 处理超时
}
```
连接的默认超时时间设置为 10s, 服务端处理的默认超时时间不设置。
```go
var DefaultOption = &Option{
    MagicNumber:       MagicNumber,
    CodecType:         codec.GobType,
    ConnectionTimeout: time.Second * 10, // 连接超时默认设置为 10秒
}
```

接下来为客户端实现的 Dial 方法加入超时处理的逻辑。将 Dial 原有的逻辑抽离到一个新的方法 dialTimeout 中，并家诶超时处理逻辑。

具体的，将创建连接的 net.Dial 替换为 net.DialTimeout，如果连接创建超时，将返回错误。然后使用子协程执行 NewClient 方法，执行完毕后通过 channel 发送结果。
```go
conn, err := net.DialTimeout(network, address, opt.ConnectionTimeout)
```
```go
type clientResult struct {
    client *Client
    err    error
}
// 创建 clientResult 类型的管道
ch := make(chan clientResult)
// 使用子协程执行 NewClient，执行完成后则通过信道 ch 发送结果
go func() {
    // f 是 NewClient 函数
    client, err := f(conn, opt)
    ch <- clientResult{
        client: client,
        err:    err,
    }
}()
```
如果设置的超时时间为 0 就直接返回客户端。否则如果 time.After() 这个 channel 先收到了结果，说明创建客户端连接超时。
```go
// 如果有超时时间则按照是否超时返回结果
select {
// time.After() 信道先接收到消息，则说明 NewClient 执行超时，返回错误
case <-time.After(opt.ConnectionTimeout):
    return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectionTimeout)
case result := <-ch:
    return result.client, result.err
}
```
最后在 Dial 方法中调用 dialTimeout 即可。
```go
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
    return dialTimeout(NewClient, network, address, opts...)
}
```

### 2、实现调用过程超时处理
客户端的调用超时使用 context 包实现，将控制权交给用户更为灵活。
```go
func (client *Client) Call(ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
    call := client.GenerateCall(serviceMethod, args, reply, make(chan *Call, 1))
    // context 包主要是⽤来处理多个 goroutine 之间共享数据，及多个 goroutine 的管理
    // Done() 返回⼀个只能接受数据的channel类型，当该context关闭或者超时时间到了的时候，该channel就会有⼀个取消信号
    // Context 对象是线程安全的，可以把⼀个 Context 对象传递给任意个数的 gorotuine，对它执⾏ 取消 操作时，所有 goroutine 都会接收到取消信号
    select {
    // 超时后会收到一个取消的信号
    case <-ctx.Done():
        client.removeCall(call.Seq)
        return errors.New("rpc client: call failed: " + ctx.Err().Error())
    // 正常调用
    case call := <-call.Done:
        return call.Error
    }
}
```
用户可以使用 context.WithTimeout 创建具备超时检测能力的 context 对象来控制。
```go
// 客户端设置超时时间为 1s，服务端无限制
t.Run("client timeout", func(t *testing.T) {
    client, _ := Dial("tcp", addr)
    // 用户实现对 Dial 的超时控制
    ctx, _ := context.WithTimeout(context.Background(), time.Second)
    var reply int
    err := client.Call(ctx, "Bar.Timeout", 1, &reply)
    _assert(err != nil && strings.Contains(err.Error(), ctx.Err().Error()), "expect a timeout error")
})
```

### 3、实现服务端超时处理
服务端的超时处理和客户端较接近，time.After() 结合 select + chan 完成。值得注意的是在这里需要保证 sendResponse 仅调用一次。因此需要将 handleRequest 方法的逻辑拆分为 called 和 sent 两个阶段。

创建两个 channel 对应 called 和 sent 阶段。
```go
called := make(chan struct{})
sent := make(chan struct{})
```
首先发起调用，不管调用是否成功，只要发起了调用就向 called 管道写入占位数据。
```go
err := req.svc.call(req.mtype, req.argv, req.replyv)
// call 了一次就向 called 管道写入 struct{}{}
called <- struct{}{}
```
如果调用没有发生错误，那就正常执行发送过程。如果发生了错误，那就将错误信息写入到响应数据，并调用发送数据的方法。只要调用了发送数据的方法，就向 sent 管道写入占位数据。
```go
// call 没发生错误
server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
sent <- struct{}{}
```
接下来的逻辑和客户端连接超时部分的逻辑是一样的。
```go
// 设置了超时时间
select {
// time.After() 先于 called 接收到消息，说明处理已经超时,called 和 sent 都将被阻塞
case <-time.After(timeout):
    req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
    server.sendResponse(cc, req.h, invalidRequest, sending)
// called 信道接收到消息，代表处理没有超时, 从 sent 管道读取数据，不会阻塞。
case <-called:
    <-sent
}
```

## 七、支持 HTTP 协议
Web 开发中，我们经常使用 HTTP 协议中的 HEAD、GET、POST 等方式发送请求，等待响应。但 RPC 的消息格式与标准的 HTTP 协议并不兼容，在这种情况下，就需要一个协议的转换过程。HTTP 协议的 CONNECT 方法恰好提供了这个能力，CONNECT 一般用于代理服务。

下面介绍一下 CONNECT 的具体使用场景。

假设浏览器与服务器之间的 HTTPS 通信都是加密的，浏览器通过代理服务器发起 HTTPS 请求时，由于请求的站点地址和端口号都是加密保存在 HTTPS 请求报文头中的，代理服务器如何知道往哪里发送请求呢？

解决方式就是浏览器通过 HTTP 明文形式向代理服务器发送一个 CONNECT 请求告诉代理服务器目标地址和端口，代理服务器接收到这个请求后，会在对应端口与目标站点建立一个 TCP 连接，连接建立成功后返回 HTTP 200 状态码告诉浏览器与该站点的加密通道已经完成。接下来代理服务器仅需传送浏览器和服务器之间的加密数据包即可，无需解析 HTTPS 报文。

这里举一个简单的例子：这个过程其实是一个将 HTTP 协议转化为 HTTPS 协议的过程。

浏览器向代理服务器发送 CONNECT 请求。
```text
CONNECT xxx.com:443 HTTP/1.0
```
代理服务器返回 HTTP 200 状态码表示连接已经建立。
```text
HTTP/1.0 200 Connection Established
```
之后浏览器和服务器之间就可以使用 HTTPS 握手并交换加密数据了，代理服务器只负责传输，不能解析数据。

仿照上面的例子，我们也可以用类似的方式，将 HTTP 协议转化为 RPC 协议。对于 RPC 服务端，要做的是将 HTTP 协议转化为 RPC 协议。对于客户端来说，要增加通过 HTTP CONNECT 创建连接的逻辑。实现完成后，通信的过程如下：

客户端向 RPC 服务端发送 CONNECT 请求.
```text
CONNECT 10.0.0.1:9999/_gamblerRPC_ HTTP/1.0
```
RPC 服务器返回 HTTP 200 状态码表示连接已经建立。
```text
HTTP/1.0 200 Connection Established
```
客户端使用创建好的连接发送 RPC 报文，先发送 Option，再发送 N 个请求报文，服务端处理 RPC 请求并响应。

### 1、服务端实现将 HTTP 转化为 RPC 协议
目的是通过 HTTP 请求的形式来访问 RPC 的响应。我们这里要做的就是实现一个 ServeHTTP 方法，因为服务端实例实现了这个方法才能作为 HTTP 某个路径的 handle。ServeHTTP 做的事情是查看接收到的请求是不是 CONNECT 类型的，然后使用 Hijack 方法让调用者来接管 HTTP 请求。
```go
conn, _, err := w.(http.Hijacker).Hijack()
```
使用了该方法后，需要调用者来管理关闭和生存期限，且不得使用原始 Request.Body。这能够让返回的内容不需要按照 HTTP 协议的规定来构造。
```go
_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
server.ServerConn(conn)
```
连接被接管后，返回一个连接成功的信息，并将这个连接用于 RCP 请求和响应的传输。最后设置对应网址的处理函数，其实就是整个服务端作为了 handle。
```go
func (server *Server) HandleHTTP() {
    http.Handle(defaultRPCPath, server)
}
```
### 2、客户端实现发送 CONNECT 请求支持 HTTP 协议
客户端要实现发起 CONNECT 请求并检查状态码。创建一个 支持 HTTP 的客户端只需要在原来创建 rpc 客户端的基础之上添加发送 CONNECT 请求，检查响应并拿到 conn 连接即可。将这部分逻辑抽离到 NewHTTPClient 中。
```go
// 向 conn 中写入请求的信息内容
_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))
// 利用建立的 TCP 连接 conn 发送一个 CONNECT 方式的 http 请求，服务端将会对 conn 进行改造并返回响应
resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
// 如果成功收到响应，那么表示 HTTP 连接已经建立，此时 rpc 的请求可以通过 HTTP 的请求方式进行传递
if err == nil && resp.Status == connected {
    return NewClient(conn, opt)
}
```
与创建 rpc 客户端类似，封装一个建立连接的方法 DialHTTP 只不过这次创建客户端的构造函数变成了支持 HTTP 协议的。
```go
return dialTimeout(NewHTTPClient, network, address, opts...)
```

最后封装一个方法用于选择客户端的连接方式 SelectDial。
```go
func SelectDial(rpcAddr string, opts ...*Option) (*Client, error)
```
### 3、利用 HTTP 协议提供其他功能
支持了 HTTP 协议后，我们可以在其他路径上提供不同的服务。比如提供一个页面来显示某个服务的详细信息和调用次数。

定义一个 debugService 结构，用于保存服务的名字和方法的名字。每一个 debugService 对应一个 服务。
```go
// 保存所有注册的 services
type debugService struct {
    Name   string
    Method map[string]*methodType
}
```
然后定义一个 debugHTTP 结构，给这个结构的实例化对象实现 ServeHTTP 方法，作为提供 HTTP 路由的 handle。这个对象要嵌套服务端实例，是一个嵌套类型，因为需要用到服务端对象的数据。
```go
type debugHTTP struct {
    *Server
}
```

ServeHTTP 的逻辑就是遍历服务端的每一个服务，将服务保存到 debugService 类型的切片 services 中。然后编写一个 html 页面，将服务详细信息通过模板渲染出来并返回。
```go
// 提供 services 数据
err := debug.Execute(w, services)
```
通过 {{.Name}}、{{range $name, $mtype := .Method}} 等等将信息渲染到 HTML 中。具体见 debug.go

## 八、负载均衡
为了提高系统的吞吐量，可以让相同功能的多个实例部署在不同的物理机器上。客户端可以选择任意一个进行调用拿到结果，所以客户端如何选择就成为了一个需要解决的问题。这个问题取决于采用什么样的负载均衡策略：
    
    随机选择
    轮询算法：依次调用
    加权轮询算法：性能更高的物理机可以承载更多的请求
    哈希一致性算法：在分布式缓存的设计中有用到

### 1、实现服务发现功能
负载均衡适用于多个服务实例的情况下，所以首先要设计实现发现服务的功能。服务发现要支持负载均衡策略的选择，所以定义一个变量用于选择负载均衡策略。然后定义两种复杂均衡策略，分别是随机和轮询。

接着要定义一个接口，接口内包含了一个服务发现模块需要的基本方法.
```go
type Discovery interface {
    Refresh() error                      // 从注册中心更新服务列表
    Update(servers []string) error       // 手动更新服务列表
    Get(mode SelectMode) (string, error) // 根据负载均衡策略选择一个服务实例
    GetAll() ([]string, error)           // 返回所有的实例
}
```

然后就是定义一个手工维护服务列表的服务发现对象 MultiServersDiscovery，并提供构造方法。构造方法需要传入 服务器的地址，servers 类似 tcp@ip.port 的形式，也就是 RPC连接服务器的格式。

这个对象需要承载的信息如下：
```go
type MultiServersDiscovery struct {
    r       *rand.Rand // 采用时间戳作为随机数种子产生随机数的实例
    mu      sync.Mutex // 用于同步控制
    servers []string   // 多个服务器地址
    index   int        // 记录 Round Robin 算法已经轮询到的位置，为了避免每次从 0 开始
}
```

接着要实现 Discovery 接口的所有方法，MultiServersDiscovery 就具备了基本的服务发现功能。接口方法中说一下 Get() 方法。

Get() 方法接收一个负载均衡策略模式参数，在 switch 中实现这两种负载均衡策略。 
```go
func (m *MultiServersDiscovery) Get(mode SelectMode) (string, error) {
    //TODO implement me
    m.mu.Lock()
    defer m.mu.Unlock()
    // 获取服务个数
    n := len(m.servers)
    if n == 0 {
        return "", errors.New("rpc discovery: no available servers")
    }
    switch mode {
    case RandomSelect:
        // Intn(n) 限定范围 0 - n
        return m.servers[m.r.Intn(n)], nil
    case RoundRobinSelect:
        //服务器可以更新，所以模式n以确保安全
        s := m.servers[m.index%n]
        m.index = (m.index + 1) % n
        // 看下 s 到底是什么
        fmt.Printf("discovery.gp -> Get -> s = %s", s)
        return s, nil
    default:
        return "", errors.New("rpc discovery: not supported select mode")
    }
}
```

### 2、可以选择不同实例的客户端
支持负载均衡的客户端需要承载以下信息：客户端还要实现一个 Close 接口方法，用于关闭客户端连接。
```go
type LBClient struct {
    dis     Discovery
    mode    SelectMode
    opt     *Option
    mu      sync.Mutex
    clients map[string]*Client // 为了尽量地复用已经创建好的 Socket 连接，使用 clients 保存创建成功的 Client 实例
}
```
接下来实现建立连接的功能，代码抽象到 dial 方法中。在 dial 中复用了之前客户端的一些方法。建立连接的逻辑是，首先查看客户端缓存中是否有能使用的客户端，如果有就直接使用这个客户端，如果没有客户端或者拿到的客户端不可用就删除这个客户端并创建新的客户端，然后保存到 map 中。其中创建客户端的方法使用了之间使用的 SelectDial 方法。
```go
client, err = SelectDial(rpcAddr, xc.opt)
```
call() 方法实现调用的逻辑。首先调用 dial 拿到客户端，然后调用客户端的 Call() 方法发起一次对 rpc 服务端的请求。 最后封装一个 call 方法的调用接口 Call() 给用户使用。

作为客户端，还有一个常用的广播功能需要实现。广播的作用是将请求广播到所有的服务实例，如果有任意一个发生错误则返回错误。如果任意一个调用成功则返回结果。
```go
func (xc *LBClient) Broadcast(ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error
```
处于性能的考虑，请求的并发的，所以要使用互斥锁保证 error 和 reply 能正确赋值。同时借助 context.WithCancel 确保有错误发生时快速失败。

### 3、测试
启动 RPC 服务的方法逻辑没有变化，只是我们这一次要启动两个服务端并注册相同的服务。另外封装一个 foo 方法用于在 单个调用 Call 和 广播调用 Broadcast 之后统一打印日志。
```go
func foo(xc *lbclient.LBClient, ctx context.Context, typ string, serviceMethod string, args *Args) {
    var reply int
    var err error
    switch typ {
    case "call":
        // 实际调用
        err = xc.Call(ctx, serviceMethod, args, &reply)
    case "broadcast":
        // 实际调用
        err = xc.Broadcast(ctx, serviceMethod, args, &reply)
    }
    // 打印错误或者成功信息
    if err != nil {
        log.Printf("%s %s error: %v", typ, serviceMethod, err)
    } else {
        log.Printf("%s %s success: %d + %d = %d", typ, serviceMethod, args.Num1, args.Num2, reply)
    }
}
```

实现单个调用的方法 call。这里的逻辑就是创建服务发现模块和支持负载均衡的客户端。然后发起调用，正常结果是只返回一次调用结果。

```go
func call(addr1 string, addr2 string) {
    // 对应两个机器地址，服务发现
    dis := lbclient.NewMultiServersDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
    // 采用随机选择的负载均衡策略客户端
    xc := lbclient.NewXClient(dis, lbclient.RandomSelect, nil)
    defer func() { _ = xc.Close() }()
    // 发送请求和接收响应
    var wg sync.WaitGroup
    for i := 0; i < 5; i++ {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            foo(xc, context.Background(), "call", "Foo.Sum", &Args{
                Num1: i,
                Num2: i * i,
            })
        }(i)
    }
    wg.Wait()
}
```

广播调用类似，并且添加了对超时处理的测试能否成功，调用了 sum 和 sleep 方法。正常的结果应该是 sum 方法返回五个调用结果，sleep 只返回两个，剩下的因为其中一个超时，导致后续所有的都提前调用终止了。

```go
func broadcast(addr1 string, addr2 string) {
    dis := lbclient.NewMultiServersDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
    xc := lbclient.NewXClient(dis, lbclient.RandomSelect, nil)
    defer func() { _ = xc.Close() }()
    
    // 发送请求和接收响应
    var wg sync.WaitGroup
    for i := 0; i < 5; i++ {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            foo(xc, context.Background(), "broadcast", "Foo.Sum", &Args{
                Num1: i,
                Num2: i * i,
            })
            ctx, _ := context.WithTimeout(context.Background(), time.Second*2)
            foo(xc, ctx, "broadcast", "Foo.Sleep", &Args{
                Num1: i,
                Num2: i * i,
            })
        }(i)
    }
    wg.Wait()
}
```

测试结果符合预期：
```text
start rpc server on [::]:53699
rpc server: register Foo.Sleep
start rpc server on [::]:53700
rpc server: register Foo.Sum
rpc server: register Foo.Sleep
rpc server: register Foo.Sum
call Foo.Sum success: 0 + 0 = 0
call Foo.Sum success: 4 + 16 = 20
call Foo.Sum success: 2 + 4 = 6
call Foo.Sum success: 3 + 9 = 12
call Foo.Sum success: 1 + 1 = 2
broadcast Foo.Sum success: 0 + 0 = 0
broadcast Foo.Sum success: 2 + 4 = 6
broadcast Foo.Sum success: 4 + 16 = 20
broadcast Foo.Sum success: 3 + 9 = 12
broadcast Foo.Sum success: 1 + 1 = 2
broadcast Foo.Sleep success: 0 + 0 = 0
broadcast Foo.Sleep success: 1 + 1 = 2
broadcast Foo.Sleep error: rpc client: call failed: context deadline exceeded
broadcast Foo.Sleep error: rpc client: call failed: context deadline exceeded
broadcast Foo.Sleep error: rpc client: call failed: context deadline exceeded
```

## 九、设置注册中心
注册中心也是一个 RPC 框架应该具有的基础功能。有了注册中心，客户端和服务端就不需要感知彼此的存在了，只需要感知注册中心即可。

服务端启动后注册服务并向注册中心发送消息告知服务已经启动，并定时向注册中心发送心跳信息证明服务可用。

客户端向注册中心询问哪些服务是可用的，注册中心返回一个可用服务列表。客户端选择其中一个发起调用。

有了注册中心就不需要在客户端硬编码服务端的地址了。主流的注册中心 etcd 、zookeeper 等工功能强大，为了对接简单自己实现一个支持心跳检测的注册中心。

### 1、设置注册中心
注册中心需要承载的信息如下：
```go
type Registry struct {
    timeout time.Duration          // 设置超时时间
    mu      sync.Mutex             // 同步控制
    servers map[string]*ServerItem // 服务的信息，包括服务所在的地址和启动时间等
}
```
其中 servers 保存的是服务器的地址，key 是地址，value 这个地址上承载的服务信息，包括：
```go
type ServerItem struct {
    Addr  string    // 服务存在于哪一个服务端
    start time.Time // 开始时间
}
```
创建注册中心，并设置服务的默认期限是 5分钟。并提供一个采用默认超时时间的注册中心构造方法。

有了注册中心，接下来要能够将服务地址添加到注册中心和返回当前可用服务地址的列表。

添加服务赋值地址很简单，根据传入的地址，添加地址和服务的映射关系。如果这个地址没有对应的服务，那就创建新的映射关系，如果已经存在了那就更新一下服务的启动时间。
```go
func (r *Registry) putServer(addr string)
```
返回可应服务地址的列表。将注册中心的 servers 这个 map 中的所有服务地址返回。在返回之前要先检查是否过期，过期则删除这个服务地址。
```go
func (r *Registry) aliveServers() []string
```

接着实现一个向注册中心发送心跳信息的方法，这个方法是一个普通的方法，仅仅只是通过 registry 包来调用，参数是注册中心以及要发送心跳信息的服务器地址。
```go
func sendHeartBeat(registry string, addr string) error 
```
实现逻辑是创建一个 HTTP 客户端并发起一次 POST 请求，这个 HTTP 请求的头部中通过 gamblerRPC-Server-Alive 字段承载发送心跳的服务地址。
```go
httpClient := &http.Client{}
req, _ := http.NewRequest("POST", registry, nil)
req.Header.Set("gamblerRPC-Server-Alive", addr)
_, err := httpClient.Do(req)
```

将这段逻辑封装在 HeartBeat 方法中暴露给用户进行调用，并添加一些额外的逻辑。
```go
func HeartBeat(registry string, addr string, duration time.Duration)
```
调用时会传入一个发送心跳消息的间隔时间，如果为 0 则设置为比服务生存期少 1 分钟。然后发送心跳消息。同时启动一个 子协程，每隔一段时间就发送一次心跳消息，心跳消息可以更新服务的启动时间，从而保证服务的存活。

最后实现一个 ServeHTTP 方法，通过 HTTP 提供服务。如果是 Get 请求则返回所有可用服务列表。POST 请求则添加服务或者发送心跳。这个逻辑通过 switch 实现。
```go
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
    switch req.Method {
    case "GET":
    // Get：返回所有可用的服务列表，通过自定义字段 gamblerRPC-Servers 承载
    w.Header().Set("gamblerRPC-List", strings.Join(r.aliveServers(), ","))
    case "POST":
    // Post：添加服务实例或发送心跳，通过自定义字段 gamblerRPC-Servers 承载
    addr := req.Header.Get("gamblerRPC-Alive")
    if addr == "" {
        w.WriteHeader(http.StatusInternalServerError)
        return
    }
    r.putServer(addr)
    default:
    w.WriteHeader(http.StatusMethodNotAllowed)
    }
}
```
注册中心采用 HTTP 提供服务，所以要给 Registry 实现 ServeHTTP 方法，并将可用列表信息通过 gamblerRPC-Servers 字段承载。添加服务地址或者给注册中心发送心跳消息采用 gamblerRPC-Server-Alive 字段承载。

### 2、支持注册中心的客户端
客户端首先需要一个支持注册中心的服务发现模块 RegistryCenterDiscovery，这个模块嵌套了之前实现的手动更新可用服务列表的服务发现模块，有一些功能可以复用。提供构造方法并设置默认 10 秒后需要重新获取可用服务地址列表。
```go
type RegistryCenterDiscovery struct {
    *MultiServersDiscovery               // 功能复用
    registry               string        // 注册中心的地址
    timeout                time.Duration // 服务列表的超时时间
    lastUpdate             time.Time     // 最后从注册中心更新服务列表的时间，默认10秒过期
}
```
接下来去实现 Discovery 的接口方法。其中 Get 方法和 GetALL 方法复用 MultiServersDiscovery 模块实现的方法，在调用之前要先调用 Refresh 方法刷新服务列表。 Update 方法和之前实现的类似，只是多加了一个更新最后更新服务列表的时间的逻辑。

最关键的是 Refresh 方法，这里是超时重新获取服务的逻辑。整体来说是检查 lastUpdate 变量，如果已经超时，则向注册中心发起一次 Get 请求来获取响应值。这个响应值里面的 gamblerRPC-Server-List 字段保存的是以 , 分割的可用服务地址列表。然后遍历这些服务地址，保存到注册中心的 servers 映射中，并更新最后刷新时间 lastUpdate。
```go
func (d *RegistryCenterDiscovery) Refresh() error {
    //TODO implement me
    d.mu.Lock()
    defer d.mu.Unlock()
    // 如果已经超时
    if d.lastUpdate.Add(d.timeout).After(time.Now()) {
        return nil
    }
    log.Println("rpc registry: refresh servers from registry", d.registry)
    // 从注册中心地址请求服务列表
    resp, err := http.Get(d.registry)
    if err != nil {
        log.Println("rpc registry refresh err:", err)
        return err
    }
    // 拿到服务列表
    servers := strings.Split(resp.Header.Get("gamblerRPC-List"), ",")
    d.servers = make([]string, 0, len(servers))
    for _, server := range servers {
        if strings.TrimSpace(server) != "" {
            d.servers = append(d.servers, strings.TrimSpace(server))
        }
    }
    d.lastUpdate = time.Now()
    return nil
}
```
### 3、进行测试
添加一个启动注册中心的方法，在这个方法中会启动注册中心服务,并将注册中心映射到 /registry/_gamblerRPC_ 路径上。
```go
func startRegistry(wg *sync.WaitGroup) {
    l, _ := net.Listen("tcp", ":9999")
    registry.HandleHTTP()
    wg.Done()
    _ = http.Serve(l, nil)
}
```
对启动 RPC 服务的方法进行修改，在启动服务并完成注册后添加一个发送心跳信息的逻辑。
```go
registry.HeartBeat(registryAddr, "tcp@"+l.Addr().String(), 0)
```
最后在 call 和 broadcast 方法中不在使用硬编码将服务器地址作为参数传递给 手动的服务发现模块，而是直接调用注册中心服务发现模块，参数变成了服务中心的地址。
```go
dis := lbclient.NewMultiServersDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
↓ ↓ ↓ ↓ ↓
dis := lbclient.NewRegistryCenterDiscovery(registry, 0)
```
在 main 函数中，首先启动注册中心，然后在两个地址上启动 RPC 服务，最后调用 call 和 broadcast 方法。

测试结果如下：
```text
rpc registry path: /_gamblerRPC_/registry
start rpc server on [::]:64554
start rpc server on [::]:64553
rpc server: register Foo.Sleep
rpc server: register Foo.Sum
rpc server: register Foo.Sleep
rpc server: register Foo.Sum
tcp@[::]:64553 send heart beat to registry http://localhost:9999/_gamblerRPC_/registry
tcp@[::]:64554 send heart beat to registry http://localhost:9999/_gamblerRPC_/registry
rpc registry: refresh servers from registry http://localhost:9999/_gamblerRPC_/registry
call Foo.Sum success: 4 + 16 = 20
call Foo.Sum success: 2 + 4 = 6
call Foo.Sum success: 1 + 1 = 2
call Foo.Sum success: 3 + 9 = 12
call Foo.Sum success: 0 + 0 = 0
rpc registry: refresh servers from registry http://localhost:9999/_gamblerRPC_/registry
broadcast Foo.Sum success: 4 + 16 = 20
broadcast Foo.Sum success: 1 + 1 = 2
broadcast Foo.Sum success: 0 + 0 = 0
broadcast Foo.Sum success: 2 + 4 = 6
broadcast Foo.Sum success: 3 + 9 = 12
broadcast Foo.Sleep success: 0 + 0 = 0
broadcast Foo.Sleep success: 1 + 1 = 2
broadcast Foo.Sleep error: rpc client: call failed: context deadline exceeded
broadcast Foo.Sleep error: rpc client: call failed: context deadline exceeded
broadcast Foo.Sleep error: rpc client: call failed: context deadline exceeded
```