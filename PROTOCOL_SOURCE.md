# 协议来源

登录与 HTTP 签名协议来源：[nethard-project/nethard-core-channel](https://github.com/nethard-project/nethard-core-channel)。本项目依据 `nethard-core-main.zip` 中的必要协议适配为 Go。该压缩包 SHA-256：

```text
d5acb057accba9c009cb3dd228cb0d50436585565b1e880b0ba3e9862f8231c5
```

压缩包声明 `nethard-core` 0.0.1、AGPL-3.0-or-later。仓库公开版本与该压缩包的模块范围有差异，此处记录实际输入版本，不将其关联到未经核实的提交。

## 使用范围

- `src/game/g79/client.ts`：PE 登录结构、设备参数、seed 及请求头。
- `src/crypto/index.ts`：V4 登录加密、V4/V12 同密钥池响应解密、PE 签名与 HTTP token 签名。
- `src/random/index.ts`：必要随机值生成。
- `src/channel/4399/client.ts`：仅 `ClientPCImpl.getSAuthJson` 的现有 Cookie 换取 sauth 流程。

未包含账号密码登录、4399 注册、短信、实名、验证码处理或服务器联机协议。狐狸聊天与历史查询的接口映射由本项目实现。

修改日期：2026-10-07。保留上游许可文本于 `LICENSE`，本项目按 AGPL-3.0-or-later 提供，无担保。管理页提供来源链接、许可证和当前版本源码下载。

## 协议参数

PE engine `3.9.5.297103`、patch `3.9.9.297335`，签名 index `3` / loopTimes `6`。登录消息按 engine、libMinecraftpeSoHash、patch、patchHash、apkSignHash、seed 拼接。采用原实现的 `pay_channel: "dashen_cloudgame"`，`sa_data` 保留末尾换行及 `start_type: "defualt"` 拼写。Go 序列化设备对象时字段排序可能不同，值保持一致。

HTTP 签名使用实际发送的 JSON 字节与完整路径。登录响应校验 UID/token，并兼容 UID 的数字/字符串表示。

4399 网页 Cookie 经固定的 `ptlogin.4399.com` 与 `microgame.5054399.net` 接口换取 `4399pc` sauth；请求使用 HTTPS。授权返回的 URL 只读取查询参数，不跟随跳转；不向 SDK 信息接口发送原始 Cookie。已生成的 `4399pc` / `4399com` 游戏 sauth 直接走 PE 登录。

游戏登录态在网关内存中缓存 30 分钟，过期后重新登录；未引入自动刷新、重连或退出协议。

## 验证与分发

网易登录、狐狸回答及 OpenAI JSON/SSE 已进行在线验证。4399 的 Cookie → sauth → PE 登录 → 聊天/历史链路通过模拟接口测试。4399 通道尚未获得真实登录成功验证。自动化测试中的全部身份、Cookie 与 token 均为无效的合成样例。

仓库、源码下载及 Release 均不包含真实账号、Cookie、密钥、运行数据、私人日志或旧协议项目。首次启动自动生成本地密钥，账号池为空。
