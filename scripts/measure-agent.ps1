# Measures tunas-agent memory and CPU (PRD section 9: RSS <= 40 MB, CPU avg < 1%).
# Usage: powershell -ExecutionPolicy Bypass -File scripts\measure-agent.ps1 [-Minutes 10] [-Name tunas-agent]
param(
    [int]$Minutes = 10,
    [string]$Name = "tunas-agent",
    [int]$IntervalSeconds = 10
)

$p = Get-Process -Name $Name -ErrorAction SilentlyContinue | Select-Object -First 1
if (-not $p) { Write-Error "process '$Name' is not running"; exit 1 }

$cores = [Environment]::ProcessorCount
$samples = [int]($Minutes * 60 / $IntervalSeconds)
$startCpu = $p.TotalProcessorTime
$start = Get-Date
$maxWs = 0; $maxPriv = 0; $sumWs = 0

Write-Host "Measuring $Name (pid $($p.Id)) for $Minutes min..."
for ($i = 0; $i -lt $samples; $i++) {
    Start-Sleep -Seconds $IntervalSeconds
    $p.Refresh()
    if ($p.HasExited) { Write-Error "process exited"; exit 1 }
    $ws = $p.WorkingSet64 / 1MB
    $priv = $p.PrivateMemorySize64 / 1MB
    $sumWs += $ws
    if ($ws -gt $maxWs) { $maxWs = $ws }
    if ($priv -gt $maxPriv) { $maxPriv = $priv }
}

$elapsed = ((Get-Date) - $start).TotalSeconds
$cpuSec = ($p.TotalProcessorTime - $startCpu).TotalSeconds
$cpuPct = 100 * $cpuSec / ($elapsed * $cores)

"{0,-28} {1,8:N1} MB" -f "Working set (avg)", ($sumWs / $samples)
"{0,-28} {1,8:N1} MB" -f "Working set (max)", $maxWs
"{0,-28} {1,8:N1} MB" -f "Private bytes (max)", $maxPriv
"{0,-28} {1,8:N2} %  (of all $cores cores)" -f "CPU (avg)", $cpuPct
"{0,-28} {1,8:N2} %  (of one core)" -f "CPU (avg, one core)", ($cpuPct * $cores)
