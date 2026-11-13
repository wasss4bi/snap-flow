param(
    [string]$Project = 'snapflow-local',
    [string]$BaseUrl = 'http://localhost:8080',
    [string]$MailpitUrl = 'http://localhost:8025'
)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http
if (([Uri]$BaseUrl).Host -notin @('localhost','127.0.0.1','::1')) { throw 'Recovery checks require a loopback-only local installation.' }

function Compose-Control([string[]]$Arguments) {
    & docker compose -p $Project @Arguments | Out-Host
    if ($LASTEXITCODE) { throw "Compose operation failed: $($Arguments[0])" }
}
function Get-QueueState {
    $lines = @(& docker compose -p $Project exec -T rabbitmq gosu rabbitmq rabbitmqctl -q list_queues name messages_unacknowledged messages_ready --formatter json)
    if ($LASTEXITCODE) { throw 'Could not inspect local queue state.' }
    $queues = ($lines -join "`n") | ConvertFrom-Json
    return @($queues | Where-Object { $_.name -eq 'snapflow.v2.tasks' })[0]
}
function Wait-Until([string]$Label, [scriptblock]$Check, [int]$Seconds = 90) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        if (& $Check) { return }
        Start-Sleep -Milliseconds 200
    }
    throw "Timed out: $Label"
}
function Get-Hash([byte[]]$Bytes) {
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return [BitConverter]::ToString($sha.ComputeHash($Bytes)) } finally { $sha.Dispose() }
}

# Refuse to run fault injection with cloud storage or a different app layout.
& docker compose -p $Project exec -T server sh -c 'test "$STORAGE_DRIVER" = local'
if ($LASTEXITCODE) { throw 'Recovery checks require the local storage backend.' }
$email = 'recovery-' + [guid]::NewGuid().ToString('N') + '@example.test'
$registration = @{name='Local recovery check'; email=$email; password='local-recovery-password'} | ConvertTo-Json
$auth = Invoke-RestMethod -Uri "$BaseUrl/api/register" -Method Post -ContentType 'application/json' -Body $registration
if (-not $auth.success) { throw 'Test registration failed.' }
$mailSearch = Invoke-RestMethod -Uri "$MailpitUrl/api/v1/search?query=$([Uri]::EscapeDataString('to:' + $email))"
if (-not $mailSearch.messages -or $mailSearch.messages.Count -eq 0) { throw 'Verification email missing.' }
$mailMessage = Invoke-RestMethod -Uri "$MailpitUrl/api/v1/message/$($mailSearch.messages[0].ID)"
if ($mailMessage.Text -notmatch 'token=([a-f0-9]{64})') { throw 'Verification token missing.' }
$confirmation = @{token=$Matches[1]} | ConvertTo-Json
$confirmed = Invoke-RestMethod -Uri "$BaseUrl/api/verify-email" -Method Post -ContentType 'application/json' -Body $confirmation
if (-not $confirmed.success) { throw 'Email confirmation failed.' }
$login = @{email=$email;password='local-recovery-password'} | ConvertTo-Json
$auth = Invoke-RestMethod -Uri "$BaseUrl/api/login" -Method Post -ContentType 'application/json' -Body $login
if (-not $auth.success -or -not $auth.token) { throw 'Test login failed.' }
$tokenLines = @(& docker compose -p $Project exec -T postgres psql -U snap_flow -d snap_flow -v ON_ERROR_STOP=1 -Atc "SELECT t.token FROM api_tokens t JOIN users u ON u.token_id=t.id WHERE u.email='$email'")
if ($LASTEXITCODE) { throw 'Could not retrieve the separate local fixture API token.' }
$apiToken = ($tokenLines -join '').Trim()
if (-not $apiToken) { throw 'Fixture API token missing.' }
$client = [Net.Http.HttpClient]::new()
$client.Timeout = [TimeSpan]::FromSeconds(20)
$filenames = [Collections.Generic.List[string]]::new()
$brokerStopped = $false
$workerStopped = $false

function Send-Images([string]$Fixture, [int]$Count) {
    $form = [Net.Http.MultipartFormDataContent]::new()
    $data = [IO.File]::ReadAllBytes($Fixture)
    try {
        for ($i=0; $i -lt $Count; $i++) {
            $part = [Net.Http.ByteArrayContent]::new($data)
            $form.Add($part, 'images', [IO.Path]::GetFileName($Fixture))
        }
        $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Post, "$BaseUrl/api/upload-images")
        $request.Headers.Authorization = [Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $apiToken)
        $request.Content = $form
        try {
            $response = $client.SendAsync($request).GetAwaiter().GetResult()
            try {
                $body = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult() | ConvertFrom-Json
                return [pscustomobject]@{Status=[int]$response.StatusCode; Body=$body}
            } finally { $response.Dispose() }
        } finally { $request.Dispose() }
    } finally { $form.Dispose() }
}

try {
    $largeFixture = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\api\bruno\fixtures\recovery.png'))
    $smallFixture = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\api\bruno\fixtures\sample.png'))
    Compose-Control @('stop','-t','0','worker')
    $workerStopped = $true
    $queued = Send-Images $largeFixture 8
    if ($queued.Status -ne 200 -or $queued.Body.files.Count -ne 8) { throw 'Could not queue the recovery batch.' }
    foreach ($name in $queued.Body.files) { $filenames.Add($name) }
    Compose-Control @('start','worker')
    $workerStopped = $false
    Wait-Until 'observe a task before acknowledgement' { (Get-QueueState).messages_unacknowledged -gt 0 } 30
    Write-Output 'Observed unacknowledged work; killing the worker before acknowledgement.'
    Compose-Control @('kill','-s','SIGKILL','worker')
    $workerStopped = $true
    Compose-Control @('up','-d','--no-build','worker')
    $workerStopped = $false
    Wait-Until 'recovered worker completes all eight images' {
        $count = Invoke-RestMethod -Uri "$BaseUrl/api/get-images-count" -Headers @{Authorization="Bearer $apiToken"}
        [int]$count.images_count -eq 8
    }
    $expectedHash = Get-Hash ([IO.File]::ReadAllBytes($largeFixture))
    foreach ($name in $filenames) {
        $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Get,"$BaseUrl/images/$name")
        $request.Headers.Authorization = [Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer',$auth.token)
        try {
            $response=$client.SendAsync($request).GetAwaiter().GetResult()
            try {
                if ([int]$response.StatusCode -ne 200) { throw 'Recovered original is not readable.' }
                $bytes=$response.Content.ReadAsByteArrayAsync().GetAwaiter().GetResult()
                if ((Get-Hash $bytes) -ne $expectedHash) { throw 'Recovered original bytes differ.' }
            } finally { $response.Dispose() }
        } finally { $request.Dispose() }
    }
    $rows=@(& docker compose -p $Project exec -T postgres psql -U snap_flow -d snap_flow -Atc "SELECT count(*) FROM users_images i JOIN users u ON u.id=i.user_id WHERE u.email='$email'")
    if ($LASTEXITCODE -or [int](($rows -join '').Trim()) -ne 8) { throw 'Recovery created duplicate metadata.' }

    Compose-Control @('stop','-t','10','rabbitmq')
    $brokerStopped=$true
    Compose-Control @('stop','-t','0','outbox')
    $rejected=Send-Images $smallFixture 1
    if ($rejected.Status -ne 200 -or -not $rejected.Body.success) { throw 'Broker outage must still permit durable outbox acceptance.' }
    Compose-Control @('up','-d','--no-build','--wait','--wait-timeout','120','rabbitmq')
    $brokerStopped=$false
    Compose-Control @('start','outbox')
    $afterRestart=Send-Images $smallFixture 1
    if ($afterRestart.Status -ne 200 -or $afterRestart.Body.files.Count -ne 1) { throw 'Publisher failed to reconnect after broker restart.' }
    $filenames.Add($afterRestart.Body.files[0])
    $filenames.Add($rejected.Body.files[0])
    Wait-Until 'consumer resubscribes and completes the post-restart image' {
        $count=Invoke-RestMethod -Uri "$BaseUrl/api/get-images-count" -Headers @{Authorization="Bearer $apiToken"}
        [int]$count.images_count -eq 10
    }
    $deleted=Invoke-RestMethod -Uri "$BaseUrl/api/delete-images" -Method Delete -Headers @{Authorization="Bearer $apiToken"}
    if (-not $deleted.success) { throw 'Could not queue test cleanup.' }
    Wait-Until 'physical test files disappear' {
        foreach ($name in $filenames) {
            if ($name -notmatch '^[0-9a-f-]+\.(jpg|jpeg|png|gif)$') { throw 'Unexpected server filename.' }
            foreach ($dir in @('uploads','thumbnails')) {
                & docker compose -p $Project exec -T worker test '!' -e "/app/data/storage/private/$dir/$name" 2>$null
                if ($LASTEXITCODE) { return $false }
            }
        }
        return $true
    }
    & docker compose -p $Project exec -T postgres psql -U snap_flow -d snap_flow -v ON_ERROR_STOP=1 -c "BEGIN; DELETE FROM email_verifications WHERE user_id=(SELECT id FROM users WHERE email='$email'); DELETE FROM users_images WHERE user_id=(SELECT id FROM users WHERE email='$email'); WITH gone AS (DELETE FROM users WHERE email='$email' RETURNING token_id) DELETE FROM api_tokens WHERE id IN (SELECT token_id FROM gone); COMMIT;" | Out-Host
    if ($LASTEXITCODE) { throw 'Local test record cleanup failed.' }
    Write-Output 'PASS: worker kill before ack recovered 8 intact images without duplicate rows; broker outage accepted durable work; publisher restart and consumer reconnection recovered both new images; physical cleanup completed.'
} finally {
    if ($brokerStopped -or $workerStopped) {
        & docker compose -p $Project up -d --no-build rabbitmq worker server outbox | Out-Host
    }
    $client.Dispose()
}
