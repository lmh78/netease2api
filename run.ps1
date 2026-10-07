param(
    [string]$Address = '127.0.0.1:18080',
    [string]$ImportFile = ''
)

$ErrorActionPreference = 'Stop'
$programPath = Join-Path $PSScriptRoot 'dist/netease2api-windows-amd64.exe'
if (-not (Test-Path -LiteralPath $programPath -PathType Leaf)) {
    $programPath = Join-Path $PSScriptRoot 'netease2api.exe'
}
if (-not (Test-Path -LiteralPath $programPath -PathType Leaf)) {
    Push-Location $PSScriptRoot
    try {
        & go build -o $programPath .
        if ($LASTEXITCODE -ne 0) { throw 'Build failed.' }
    } finally { Pop-Location }
}
$toolArguments = @('-addr', $Address, '-data', (Join-Path $PSScriptRoot 'data'))
if ($ImportFile) { $toolArguments += @('-import', $ImportFile) }
& $programPath @toolArguments
exit $LASTEXITCODE
