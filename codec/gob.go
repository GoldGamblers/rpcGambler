package codec

import (
	"bufio"
	"encoding/gob"
	"io"
	"log"
)

// GobCodec Gob 是 Go 自己的以二进制形式序列化和反序列化程序数据的格式;
// 实现对 Gob 的消息编解码器
type GobCodec struct {
	conn io.WriteCloser // 由构建函数传入，通常是通过 TCP 或者 Unix 建立 socket 时得到的链接实例
	buf  *bufio.Writer  // 防止阻塞而创建的带缓冲的 Writer
	dec  *gob.Decoder
	enc  *gob.Encoder
}

// 检查 GobCodec 是否实现了 _Codec 结构的全部内容
var _Codec = (*GobCodec)(nil)

// NewGobCodec 实例化一个 GobCodec 对象
func NewGobCodec(conn io.ReadWriteCloser) Codec {
	buf := bufio.NewWriter(conn)
	return &GobCodec{
		conn: conn,
		buf:  buf,
		dec:  gob.NewDecoder(conn),
		enc:  gob.NewEncoder(buf),
	}
}

// Close 实现 Codec 接口的方法
func (c *GobCodec) Close() error {
	//TODO implement me
	return c.conn.Close()
}

// ReadHeader 实现 Codec 接口的方法
func (c *GobCodec) ReadHeader(h *Header) error {
	//TODO implement me
	return c.dec.Decode(h)
}

// ReadBody 实现 Codec 接口的方法
func (c *GobCodec) ReadBody(body interface{}) error {
	//TODO implement me
	return c.dec.Decode(body)
}

// Write 实现 Codec 接口的方法
func (c *GobCodec) Write(h *Header, body interface{}) (err error) {
	//TODO implement me
	defer func() {
		// 先写入到 buffer 中, 然后我们再调用 buffer.Flush() 来将 buffer 中的全部内容写入到 conn 中
		// 刷新缓冲区,只要用到缓冲区,就要记得刷新
		_ = c.buf.Flush()
		if err != nil {
			_ = c.Close()
		}
	}()
	// 先执行
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
