param(
    [ValidateRange(1, 65535)]
    [int]$Port = 50000,

    [Parameter(Mandatory = $true)]
    [ValidateNotNullOrEmpty()]
    [string]$Command,

    [ValidateRange(1, 120)]
    [int]$TimeoutSeconds = 10,

    [ValidateRange(50, 5000)]
    [int]$QuietPeriodMilliseconds = 300,

    [switch]$AllowEmptyResponse
)

$ErrorActionPreference = "Stop"

$client = [System.Net.Sockets.TcpClient]::new()
try {
    $connectTask = $client.ConnectAsync("127.0.0.1", $Port)
    if (-not $connectTask.Wait([TimeSpan]::FromSeconds($TimeoutSeconds))) {
        throw "timed out connecting to MULTI socket 127.0.0.1:$Port"
    }

    $stream = $client.GetStream()
    $stream.ReadTimeout = 100
    $encoding = [System.Text.Encoding]::ASCII
    $payload = $encoding.GetBytes($Command + "`n")
    $stream.Write($payload, 0, $payload.Length)
    $stream.Flush()

    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $lastRead = [DateTime]::UtcNow
    $receivedAny = $false
    $completed = $false
    $response = [System.Text.StringBuilder]::new()
    $buffer = [byte[]]::new(8192)

    while ([DateTime]::UtcNow -lt $deadline) {
        if ($client.Client.Poll(0, [System.Net.Sockets.SelectMode]::SelectRead)) {
            $count = $stream.Read($buffer, 0, $buffer.Length)
            if ($count -eq 0) {
                $completed = $true
                break
            }

            [void]$response.Append($encoding.GetString($buffer, 0, $count))
            $receivedAny = $true
            $lastRead = [DateTime]::UtcNow
            continue
        }

        if (($receivedAny -or $AllowEmptyResponse) -and
            (([DateTime]::UtcNow - $lastRead).TotalMilliseconds -ge $QuietPeriodMilliseconds)) {
            $completed = $true
            break
        }

        Start-Sleep -Milliseconds 25
    }

    if (-not $completed) {
        throw "MULTI socket response incomplete after $TimeoutSeconds seconds"
    }

    if ((-not $receivedAny) -and (-not $AllowEmptyResponse)) {
        throw "MULTI socket returned no data"
    }

    if ($receivedAny) {
        $response.ToString().TrimEnd("`r", "`n")
    }
}
finally {
    $client.Dispose()
}
