# netease2api

将《我的世界中国版》狐狸 AI 对话接口转换为 OpenAI Chat Completions API。支持网易与 4399 Cookie、中文管理页面、多账号轮换和 API 密钥管理。

已于 2026-10-07 验证真实账号登录、普通 Chat Completions 和 SSE 响应。通过 `/pet-agent/chat` 发起请求，再查询 `/pet-agent/history` 获取回答，无需游戏客户端或 LinkServer 长连接。4399 Cookie 换取登录态的完整链路已通过模拟接口测试；尚未获得真实 4399 登录成功验证。

项目按 AGPL-3.0-or-later 提供，许可文本见 `LICENSE`。管理页底部提供协议来源、许可证和当前版本源码下载。

## 启动

Windows 已提供 `netease2api.exe`：

从 [Releases](https://github.com/lmh78/netease2api/releases) 下载系统对应的压缩包并解压。再次启动前需要停止占用同一端口的旧服务。

```powershell
cd .\netease2api
.\run.ps1
```

访问 http://127.0.0.1:18080/ 。管理密钥是 `data/access.local.json` 中的 `admin_key`，调用 API 使用其中的 `api_key`，两者权限不同。首次启动自动生成，不需要安装数据库。

程序首次运行时账号池为空。自行导入 Cookie，发布包不附带账号、密钥或登录材料。

Linux 解压后启动：

```bash
./netease2api -addr 127.0.0.1:18080 -data data
```

从源码启动（Go 1.26.6 或更高）：

```powershell
go run . -addr 127.0.0.1:18080 -data data
```

## 多 Cookie 导入

在管理页面进入「账号池 → 导入账号」，可以粘贴内容或选择 JSON / JSONL / TXT 文件。支持：

- 账号记录：`{"name":"账号名称","cookie":"..."}`，`name` 可以省略、为 `null` 或空白。
- 无名称记录：`{"cookie":"..."}`；`cookie` 也可以是 sauth 对象或 Set-Cookie 字符串数组。
- 上述记录组成的 JSON 数组。
- 原始 sauth 对象：`{"sdkuid":"...","sessionid":"...", ...}`。
- `sauth_json` 为对象或字符串的包装。
- 网易/4399 游戏生成的 sauth：4399 的渠道字段为 `4399pc` 或 `4399com`，按原样发送到 PE 登录接口。
- 含 URL 编码 `sauth_json=...` 的 Cookie 文本，每行一份，也支持 `Cookie:` 前缀。
- 4399 网页 Cookie：含 `Uauth=4399|...` 的整行 Cookie（包括关联的 `Pauth` 等字段）。登录态换取只在检查账号或首次使用时执行，不在批量导入时发送网络请求。

省略名称时自动生成名称；无名称的 Cookie 更新会保留已有自定义名称。不同渠道即使 `sdkuid` 相同也分别保存。4399 网页 Cookie 在检查前显示「等待检查」，成功后显示 SDK UID。

输入示例只表示格式，不是可用账号：

```json
{ "cookie": { "sdkuid": "YOUR_UID", "sessionid": "YOUR_SESSION", "app_channel": "4399pc", "login_channel": "4399pc", "platform": "pc" } }
```

每次最多 1000 条、HTTP 请求体最大 4 MiB；界面文件上传限制 3 MiB。整批内容验证通过后才导入。同一渠道与 `sdkuid` 会更新游戏 Cookie；相同的 4399 网页 Cookie 会更新原条目，换成另一份网页 Cookie 会创建新条目。忙碌账号拒绝更新和删除。

也可以在启动时导入文件：

```powershell
.\run.ps1 -ImportFile 'C:\实际路径\accounts.txt'
.\netease2api.exe -data data -import 'C:\实际路径\accounts.txt' -init-only
```

`-init-only` 用于服务未运行时初始化。一个 data 目录只供一个服务进程使用，运行中应使用管理接口导入。

## OpenAI 客户端配置

| 设置 | 值 |
| --- | --- |
| Base URL | `http://127.0.0.1:18080/v1` |
| API Key | 默认 `api_key` 或在管理页面新建的密钥 |
| Model | `netease-fox` |

接口按[官方 Chat Completions 格式](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)返回 JSON 和 SSE；这里只提供狐狸接口，模型名不会伪装成 GPT。

Python 客户端示例：

```python
import json
from pathlib import Path
from openai import OpenAI

keys = json.loads(Path("data/access.local.json").read_text(encoding="utf-8"))
client = OpenAI(base_url="http://127.0.0.1:18080/v1", api_key=keys["api_key"])
reply = client.chat.completions.create(
    model="netease-fox",
    messages=[{"role": "user", "content": "工作台怎么合成？"}],
)
print(reply.choices[0].message.content)

stream = client.chat.completions.create(
    model="netease-fox",
    messages=[{"role": "user", "content": "钻石有什么用？"}],
    stream=True,
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="", flush=True)
```

HTTP 示例：

```bash
curl http://127.0.0.1:18080/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"netease-fox","messages":[{"role":"user","content":"你好"}],"stream":true}'
```

## 功能与边界

- `GET /v1/models`、`POST /v1/chat/completions`，Bearer API 密钥鉴权。
- `stream: false` 返回标准完成对象；`stream: true` 返回角色、内容、结束分块和 `[DONE]`。等待期间发 SSE 心跳，完成回答后分段输出，并非模型生成期间逐字传输。
- 多账号轮换，每个账号一次只处理一个请求；忙碌时排队，总超时默认 90 秒，网关最多容纳 64 个并发请求。
- 登录失败的账号标为需要更新 Cookie；暂时连接失败冷却 30 秒。上游 `code=5` 标为达到对话上限，本地冷却 30 分钟，不能据此保证 30 分钟后上游额度恢复。
- 明确登录失败或明确限额拒绝时，最多尝试 8 个不同账号。请求发送状态不确定或已接受后查询失败，不重复发送到另一个账号。
- 游戏登录态仅在内存缓存 30 分钟。重启重新登录。
- 支持 user / assistant / system / developer 文本消息，以及 `content` 的 text 数组。多条消息完整合并后发送，每次用新的上游会话隔离请求，默认采用原游戏界面的 200 字限制，包含角色标签；超过限制返回 400，不截断。
- 图片、音频、工具调用、JSON 输出约束和 `n > 1` 明确返回 400。
- 上游不提供 tokenizer / token 统计，`usage` 为 `null`。`temperature`、`top_p`、`max_tokens` 等采样和长度参数兼容接收但不会改变上游行为；当前仅承诺文本 Chat Completions，不提供 Responses、Embeddings 或图片 API。
- 管理页面支持账号检查、启用/停用、删除、密钥创建/撤销、对话测试和请求统计。最近 200 条记录只保存调度元信息，不保存问题或回答。

## 本地保存与配置

`data/accounts.enc` 使用 AES-GCM 保存 Cookie、API 密钥哈希、账号状态和请求记录。`data/master.key` 是加密主密钥；备份时保留完整 data 目录，否则无法恢复账号。API 返回的账号列表不包含 Cookie 或游戏 token。

`data/access.local.json` 保存本机管理密钥和初始 API 密钥；数据目录、登录材料、可执行文件及日志均已加入 `.gitignore`。使用环境变量 `NETEASE2API_ADMIN_KEY` 可以覆盖管理密钥。`NETEASE2API_API_KEY` 仅用于首次初始化默认调用密钥；后续调用密钥通过管理页面创建/撤销，撤销后重启也不会重新启用。

首次部署默认只监听本机。要供其他电脑访问，可在自己的部署环境指定监听地址并配置 HTTPS 反向代理。

可配置选项：

```text
-addr              127.0.0.1:18080
-data              data
-import            批量 Cookie 文件路径
-init-only         初始化后退出
-timeout           90s
-poll-interval     2s
-upstream-api      覆盖网易 API 网关地址
-upstream-login    覆盖网易 PE 登录地址
```

网易登录与请求签名协议来源：[nethard-project/nethard-core-channel](https://github.com/nethard-project/nethard-core-channel)。实际依据用户提供的 `nethard-core-main.zip` 适配并验证，版本差异、采用范围和验证详情见 [PROTOCOL_SOURCE.md](PROTOCOL_SOURCE.md)。协议代码仅保留 Cookie/sauth 解析、4399 PC Cookie 换取 sauth、`/pe-authentication` 登录、请求加解密与签名、`/pet-agent/chat` 和 `/pet-agent/history`。当前默认服务地址为 G79 PE 正式服；其他服务区或版本是否可用需要单独验证。

## Docker

```bash
docker compose up -d --build
docker compose exec netease2api cat /data/access.local.json
```

Compose 将端口绑定到宿主本机，用命名卷持久化账号。Windows 可执行文件已验证，Linux 已交叉编译；Docker 配置尚未进行运行验证。

## 管理接口

所有 `/admin/*` 使用 `Authorization: Bearer <admin_key>`：

| 接口 | 用途 |
| --- | --- |
| `GET /admin/overview` | 概览统计 |
| `GET /admin/accounts` | 账号状态，不返回登录材料 |
| `POST /admin/accounts/import` | `{"content":"JSON / JSONL / Cookie文本"}` |
| `PATCH /admin/accounts/{id}` | `{"enabled":true}` |
| `DELETE /admin/accounts/{id}` | 删除空闲账号 |
| `POST /admin/accounts/{id}/check` | 检查登录 |
| `POST /admin/accounts/check` | 顺序检查所有启用且空闲账号 |
| `GET /admin/keys` | 列出密钥前缀和状态 |
| `POST /admin/keys` | `{"name":"应用名称"}`，完整密钥仅返回一次 |
| `DELETE /admin/keys/{id}` | 撤销调用密钥 |
| `GET /admin/logs` | 最近 200 条调度记录 |

## 验证与目录

```powershell
python scripts/source_archive.py
go test ./...
go vet ./...
go build -o netease2api.exe .
```

离线测试覆盖加密持久化与重启、批量格式解析、重复账号更新、失败批次原子性、并发账号隔离、账号轮换、登录失败切换、取消请求、API/管理权限隔离、密钥撤销、200 字和多模态校验、JSON/SSE 格式、静态页面和 CORS。

```text
main.go                   启动与配置
internal/gateway/store.go  加密存储、导入、调度与密钥
internal/gateway/upstream.go  狐狸接口桥接
internal/gateway/http.go   OpenAI / 管理 HTTP 接口
internal/gateway/web/      内嵌中文管理页面
internal/netease/          狐狸所需的 PE 登录、签名与聊天协议
scripts/source_archive.py  生成不含私有数据的嵌入源码
scripts/release.py         构建发行包与 SHA-256 校验文件
```

本项目借鉴 [Sub2API](https://github.com/Wei-Shaw/sub2api) 的多账号管理与统一 API 入口思路，当前为独立实现；没有复制其前后端代码，也不包含其完整计费、多租户或工具调用功能。

## 使用说明

此服务可能违反网易公司用户守则，造成的后果由使用者自行承担。

## 构建发行包

```bash
python scripts/release.py --version v0.3.0
```

生成 Windows / Linux 程序包、完整源码包及 `SHA256SUMS.txt` 到 `release/`。发行包采用明确文件清单，不读取运行数据或账号文件。
