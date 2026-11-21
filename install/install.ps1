<#
  install.ps1 — 在 Windows 上安装 dsync.exe（从 GitHub Release 下载并校验），然后执行 dsync host setup

  仓库公开，匿名即可安装（设置了 $env:GITHUB_TOKEN 或已登录 gh 时自动带上，用于提高 API 限额）。
  用法（管理员 PowerShell；本文件须保留 UTF-8 BOM，否则 PowerShell 5.1 会把中文读成乱码）：

    Invoke-RestMethod -OutFile "$env:TEMP\install-dsync.ps1" `
      'https://raw.githubusercontent.com/Lin-Ya/dsync-cli/main/install/install.ps1'
    powershell -ExecutionPolicy Bypass -File "$env:TEMP\install-dsync.ps1" --source D:\Develop\dsync

  其后的参数原样传给 dsync host setup；不带参数时只安装 dsync.exe。
  （不要用 irm | iex：带 BOM 的脚本经 scriptblock 执行会失败，且无法传参。）

  环境变量：
    GITHUB_TOKEN       可选：提高 API 速率限制（未认证每小时 60 次）
    DSYNC_VERSION      目标版本 tag（默认最新，含预发布）
    DSYNC_INSTALL_DIR  安装目录（默认 %LOCALAPPDATA%\dsync\bin，并加入用户 PATH）
    DSYNC_GITHUB_API   GitHub API 地址（测试用）
#>
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'   # PowerShell 5.1 的进度条会让下载慢一个数量级

# 从 PowerShell 7（pwsh，Windows Terminal 的常见默认终端）里执行 powershell -File 时，本进程会继承
# pwsh 的 PSModulePath，按需加载的命令（如 Get-FileHash）会去加载 7 的模块而失败。重置为 5.1 自己的模块目录。
if ($PSVersionTable.PSEdition -ne 'Core') {
  $env:PSModulePath = "$env:ProgramFiles\WindowsPowerShell\Modules;$PSHOME\Modules"
}

function Die($msg) { Write-Host "dsync install: $msg" -ForegroundColor Red; exit 1 }

$Repo = 'Lin-Ya/dsync-cli'
$Api = if ($env:DSYNC_GITHUB_API) { $env:DSYNC_GITHUB_API } else { 'https://api.github.com' }
$Dir = if ($env:DSYNC_INSTALL_DIR) { $env:DSYNC_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'dsync\bin' }
$Name = 'dsync_windows_amd64.exe'

# PowerShell 5.1 默认可能不启用 TLS 1.2，GitHub 需要
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

# Token 可选：公开仓库匿名也能下载；设置了就带上，提高 API 速率限制。
$Token = $env:GITHUB_TOKEN
if (-not $Token -and (Get-Command gh -ErrorAction SilentlyContinue)) {
  # gh 未登录（或已登出）时会往 stderr 写一行；$ErrorActionPreference = 'Stop' 下 PowerShell 5.1
  # 会把它变成 NativeCommandError 并终止脚本，所以这里必须接住。
  try { $Token = (& gh auth token 2>$null | Out-String).Trim() } catch { $Token = '' }
}
if ($env:PROCESSOR_ARCHITECTURE -ne 'AMD64') { Die "仅提供 windows/amd64 版本（当前 $env:PROCESSOR_ARCHITECTURE）" }

$Headers = @{ 'X-GitHub-Api-Version' = '2022-11-28' }
if ($Token) { $Headers['Authorization'] = "Bearer $Token" }

function Get-Api($url, $accept, $outFile) {
  $h = $Headers.Clone(); $h['Accept'] = $accept
  try {
    # 跳转到对象存储时 .NET 会去掉 Authorization 头，Token 不会发给第三方域名
    if ($outFile) { Invoke-WebRequest -Uri $url -Headers $h -OutFile $outFile -UseBasicParsing | Out-Null }
    else { Invoke-RestMethod -Uri $url -Headers $h }
  } catch {
    Die "请求 $url 失败（网络受限，或 Token 无效？未设置 Token 时可能触发了匿名速率限制）: $($_.Exception.Message)"
  }
}

if ($env:DSYNC_VERSION) {
  $rel = Get-Api "$Api/repos/$Repo/releases/tags/$env:DSYNC_VERSION" 'application/vnd.github+json'
} else {
  # /releases/latest 不含预发布版本，取列表中最新的一个
  $rel = Get-Api "$Api/repos/$Repo/releases?per_page=1" 'application/vnd.github+json'
  if ($rel -is [System.Array]) { $rel = $rel[0] }
}
if (-not $rel -or -not $rel.tag_name) { Die '没有找到可用的 Release' }
$tag = $rel.tag_name
$bin = $rel.assets | Where-Object { $_.name -eq $Name } | Select-Object -First 1
$sums = $rel.assets | Where-Object { $_.name -eq 'checksums.txt' } | Select-Object -First 1
if (-not $bin) { Die "Release $tag 中没有 $Name" }
if (-not $sums) { Die "Release $tag 中没有 checksums.txt" }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("dsync-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force $tmp | Out-Null
try {
  Get-Api $sums.url 'application/octet-stream' (Join-Path $tmp 'checksums.txt')
  Get-Api $bin.url 'application/octet-stream' (Join-Path $tmp $Name)

  $want = $null
  foreach ($line in Get-Content (Join-Path $tmp 'checksums.txt')) {
    $f = -split $line
    if ($f.Count -eq 2 -and ($f[1] -eq $Name -or $f[1] -eq "*$Name")) { $want = $f[0].ToLower(); break }
  }
  if (-not $want) { Die "checksums.txt 中没有 $Name" }
  $got = (Get-FileHash (Join-Path $tmp $Name) -Algorithm SHA256).Hash.ToLower()
  if ($got -ne $want) { Die "$Name 校验失败（sha256 $got，期望 $want），已拒绝安装" }

  New-Item -ItemType Directory -Force $Dir | Out-Null
  $exe = Join-Path $Dir 'dsync.exe'
  Move-Item -Force (Join-Path $tmp $Name) $exe
} finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
Write-Host "dsync $tag 已安装到 $exe"

# 加入用户 PATH（持久）与当前会话 PATH
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not (($userPath -split ';') -contains $Dir)) {
  $newPath = if ($userPath) { "$userPath;$Dir" } else { $Dir }
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  Write-Host "已把 $Dir 加入用户 PATH（新开的终端生效）"
}
if (-not (($env:Path -split ';') -contains $Dir)) { $env:Path = "$env:Path;$Dir" }

if ($args.Count -gt 0) {
  & $exe host setup @args
  exit $LASTEXITCODE
}
Write-Host "下一步（管理员 PowerShell）：dsync host setup --source <本地仓库目录>  或  dsync host setup <仓库地址>"
