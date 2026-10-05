// fakedsync 是 install.ps1 测试中被"安装"的 dsync.exe：回显参数，用于验证安装后执行了 host setup。
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	fmt.Println("fake-dsync " + strings.Join(os.Args[1:], " "))
}
