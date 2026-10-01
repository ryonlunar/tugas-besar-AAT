param(
  [int]$ApiPort = 8080,
  [int]$Seconds = 90,
  [int]$IntervalSeconds = 5,
  [string]$OutputPath = "docs/evidence/load-tcp-connections.json"
)

$samples = @()
$end = (Get-Date).AddSeconds($Seconds)
while ((Get-Date) -lt $end) {
  $count = @(Get-NetTCPConnection -State Established -RemotePort $ApiPort -ErrorAction SilentlyContinue).Count
  $samples += [PSCustomObject]@{
    timestamp = (Get-Date).ToUniversalTime().ToString("o")
    established_connections_to_api_port = $count
  }
  Start-Sleep -Seconds $IntervalSeconds
}

$samples | ConvertTo-Json | Set-Content -Encoding utf8 $OutputPath
Write-Output "Wrote TCP evidence to $OutputPath"
