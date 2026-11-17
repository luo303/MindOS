// 本文件是密码哈希与策略。
//
// 用 bcrypt 而不是自己拼 SHA-256 + salt：bcrypt 自带盐、自带成本参数，
// 且「算得慢」正是它要的效果——离线爆破的代价由 cost 决定。
// cost 12 在本机约 150–300ms，登录一次的成本可以接受，爆破一次的成本不可接受。
package auth

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	// defaultBcryptCost 是生产使用的成本参数。12 是 2020 年代硬件下的常见取值：
	// 再低（10）单次太快，再高（14）会让登录明显变卡且放大暴力破解时的 CPU 压力。
	defaultBcryptCost = 12

	// minPasswordBytes 是最短密码长度。8 是 NIST SP 800-63B 的下限，
	// 不要求大小写/符号组合——那类规则的实际效果是让人用 "Passw0rd!"。
	minPasswordBytes = 8
	// maxPasswordBytes 是 bcrypt 的硬限制：它只处理前 72 字节。
	//
	// 超长时**拒绝**而不是截断：截断会让两个不同的长密码变成同一个，
	// 用户改密码后旧密码照样能登录，这是最坏的一种"看起来正常"。
	maxPasswordBytes = 72
)

// ErrWeakPassword 表示密码长度不符合策略。
var ErrWeakPassword = errors.New("password must be 8 to 72 bytes")

// bcryptCost 是实际使用的成本参数。
//
// 用 var 而不是 const 只为一件事：测试在 TestMain 里调低它。登录相关用例
// 要算几十次哈希，cost 12 在 -race 下会让一个包多跑一分钟。生产代码没有
// 任何地方写这个变量，默认值由 TestDefaultBcryptCost 守着。
var bcryptCost = defaultBcryptCost

// dummyHash 用于"邮箱不存在"时也执行一次 bcrypt 比较。
//
// 不这么做的话，响应时间会把"这个邮箱注册过"直接告诉攻击者：
// 存在的邮箱要算一次 bcrypt（约 200ms），不存在的一瞬间就返回。
// 这一个常量就是让两条路径耗时接近的全部成本。
const dummyHash = "$2a$12$ZiDWj3jHGAR9cHUQr358QOjwlbm0R8VB5dOR9cG1QhTOcqs.TdIe2"

// HashPassword 校验策略并返回 bcrypt 哈希。
func HashPassword(plain string) (string, error) {
	if err := validatePassword(plain); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword 比对明文与哈希。不匹配时统一返回 ErrInvalidCredentials，
// 不把 bcrypt 的细节（"hashedSecret too short"）漏给调用方。
func VerifyPassword(hash, plain string) error {
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// compareDummy 在"用户不存在"时消耗与一次真实比较相当的时间。
// 返回值恒为不匹配，调用方不关心内容。
func compareDummy(plain string) {
	_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(plain))
}

// validatePassword 只校验长度。
//
// 用字节而不是码点：bcrypt 的上限就是按字节算的。中文密码 24 个字符就到 72 字节，
// 这也意味着它的可用空间更大，没有理由为它单独放宽。
func validatePassword(plain string) error {
	if len(plain) < minPasswordBytes || len(plain) > maxPasswordBytes {
		return ErrWeakPassword
	}
	// 全是空白的密码能通过长度检查，但它不是密码。
	if utf8.RuneCountInString(strings.TrimSpace(plain)) == 0 {
		return ErrWeakPassword
	}
	return nil
}
