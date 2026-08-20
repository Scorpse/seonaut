[CmdletBinding()]
param(
    [string]$BaseUrl = "http://localhost:19000",
    [string]$RollbackUrl = "http://localhost:19001",
    [string]$ComposeProject = "seonaut-campanix-smoke",
    [switch]$SkipBuild,
    [switch]$SkipRollback,
    [switch]$KeepStack,
    [switch]$ReuseStack,
    [switch]$AllowDirty
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$composeFile = Join-Path $PSScriptRoot "..\docker-compose.campanix.yml"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$compose = @("compose", "-f", $composeFile, "-p", $ComposeProject)
$rootKey = "snk_test_root.smoke-root-secret"
$upstreamRevision = "880b312c28fab8b0bf7fe4f9449dc4746dbb82ff"
$upstreamDigest = "sha256:77826fb91f1f7b3d054bcd73c4f9df40c6b40962cfab68208f435e0758266415"

function Invoke-Docker {
    param([Parameter(Mandatory)][string[]]$Arguments)
    $output = & docker @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "docker $($Arguments -join ' ') failed:`n$($output -join "`n")"
    }
    return $output
}

function Invoke-SmokeRequest {
    param(
        [Parameter(Mandatory)][string]$Method,
        [Parameter(Mandatory)][string]$Path,
        [string]$Token,
        [object]$Body,
        [hashtable]$ExtraHeaders,
        [string]$Origin = $BaseUrl
    )
    $headers = @{}
    if ($Token) { $headers.Authorization = "Bearer $Token" }
    if ($ExtraHeaders) {
        foreach ($name in $ExtraHeaders.Keys) { $headers[$name] = $ExtraHeaders[$name] }
    }
    $parameters = @{
        Method = $Method
        Uri = "$Origin$Path"
        Headers = $headers
        SkipHttpErrorCheck = $true
    }
    if ($null -ne $Body) {
        $parameters.ContentType = "application/json"
        $parameters.Body = ($Body | ConvertTo-Json -Depth 10 -Compress)
    }
    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    $response = Invoke-WebRequest @parameters
    $timer.Stop()
    $json = $null
    if ($response.Content -and $response.Headers."Content-Type" -match "application/json") {
        $json = $response.Content | ConvertFrom-Json -Depth 20
    }
    return [pscustomobject]@{
        Status = [int]$response.StatusCode
        Content = $response.Content
        Json = $json
        Headers = $response.Headers
        Milliseconds = $timer.ElapsedMilliseconds
    }
}

function Assert-Status {
    param([Parameter(Mandatory)]$Response, [Parameter(Mandatory)][int[]]$Expected, [Parameter(Mandatory)][string]$Name)
    if ($Response.Status -notin $Expected) {
        throw "$Name returned $($Response.Status): $($Response.Content)"
    }
}

function Get-DatabaseScalar {
    param([Parameter(Mandatory)][string]$Sql)
    $output = Invoke-Docker ($compose + @("exec", "-T", "-e", "MYSQL_PWD=seonaut-smoke", "db", "mysql", "-N", "-B", "-useonaut", "seonaut", "-e", $Sql))
    return [string]($output | Select-Object -Last 1)
}

function Wait-ForUrl {
    param([Parameter(Mandatory)][string]$Url, [int]$Attempts = 90)
    for ($attempt = 1; $attempt -le $Attempts; $attempt++) {
        try {
            $response = Invoke-WebRequest -Uri $Url -SkipHttpErrorCheck -TimeoutSec 3
            if ([int]$response.StatusCode -lt 500) { return }
        } catch { }
        Start-Sleep -Seconds 2
    }
    throw "Timed out waiting for $Url"
}

Push-Location $repoRoot
try {
    $forkRevision = (& git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw "Cannot resolve the fork revision" }
    if (-not $AllowDirty -and (& git status --porcelain)) {
        throw "The smoke image must be built from a clean worktree. Commit changes or pass -AllowDirty for development only."
    }
    $env:SEONAUT_FORK_REVISION = $forkRevision
    $env:SEONAUT_FORK_VERSION = "mkt-281-smoke"
    $image = "kilnbench/seonaut-campanix:$forkRevision"

    if (-not $ReuseStack) {
        Invoke-Docker ($compose + @("down", "--volumes", "--remove-orphans")) | Out-Null
        $up = @("up", "-d")
        if (-not $SkipBuild) { $up += "--build" }
        $up += @("db", "fixture", "app")
        Invoke-Docker ($compose + $up) | Out-Null
    }
    Wait-ForUrl "$BaseUrl/api/v1/health"

    $platform = Invoke-SmokeRequest "POST" "/api/v1/root/platform-keys" $rootKey @{
        scopes = @("tenants:provision", "tenant_keys:create", "meta:read")
    }
    Assert-Status $platform @(201) "platform key creation"
    $platformKey = [string]$platform.Json.data.key

    foreach ($tenant in @("tenant-a", "tenant-b")) {
        $provision = Invoke-SmokeRequest "PUT" "/api/v1/tenants/$tenant" $platformKey $null
        Assert-Status $provision @(200) "$tenant provisioning"
    }

    $tenantScopes = @("meta:read", "projects:read", "projects:write", "crawls:read", "crawls:run", "crawls:cancel", "findings:read", "exports:read", "keys:manage")
    $tenantAResponse = Invoke-SmokeRequest "POST" "/api/v1/tenants/tenant-a/keys" $platformKey @{ scopes = $tenantScopes }
    $tenantBResponse = Invoke-SmokeRequest "POST" "/api/v1/tenants/tenant-b/keys" $platformKey @{ scopes = $tenantScopes }
    Assert-Status $tenantAResponse @(201) "tenant-a key creation"
    Assert-Status $tenantBResponse @(201) "tenant-b key creation"
    $tenantAKey = [string]$tenantAResponse.Json.data.key
    $tenantBKey = [string]$tenantBResponse.Json.data.key

    $projectBody = @{ url = "http://fixture/"; crawl_sitemap = $true; check_external_links = $false; archive = $true; user_agent = "Kilnbench-SEOnaut-Smoke/1.0" }
    $projectAResponse = Invoke-SmokeRequest "PUT" "/api/v1/projects/fixture-a" $tenantAKey $projectBody @{ "Idempotency-Key" = "fixture-project-a" }
    $projectBResponse = Invoke-SmokeRequest "PUT" "/api/v1/projects/fixture-b" $tenantBKey $projectBody @{ "Idempotency-Key" = "fixture-project-b" }
    Assert-Status $projectAResponse @(201) "tenant-a project creation"
    Assert-Status $projectBResponse @(201) "tenant-b project creation"
    $projectA = [string]$projectAResponse.Json.data.id
    $projectB = [string]$projectBResponse.Json.data.id

    $crawlResponse = Invoke-SmokeRequest "POST" "/api/v1/projects/$projectA/crawls" $tenantAKey $null @{ "Idempotency-Key" = "fixture-crawl-a" }
    Assert-Status $crawlResponse @(202) "fixture crawl start"
    $crawlA = [string]$crawlResponse.Json.data.id
    $crawl = $null
    for ($attempt = 1; $attempt -le 90; $attempt++) {
        $crawlResponse = Invoke-SmokeRequest "GET" "/api/v1/projects/$projectA/crawls/$crawlA" $tenantAKey $null
        Assert-Status $crawlResponse @(200) "fixture crawl poll"
        $crawl = $crawlResponse.Json.data
        if ($crawl.state -in @("succeeded", "failed", "canceled")) { break }
        Start-Sleep -Seconds 1
    }
    if ($null -eq $crawl -or $crawl.state -ne "succeeded") {
        throw "Fixture crawl did not succeed: $($crawl | ConvertTo-Json -Depth 5 -Compress)"
    }

    $results = @{}
    foreach ($kind in @("issues", "pages", "links", "resources")) {
        $response = Invoke-SmokeRequest "GET" "/api/v1/projects/$projectA/crawls/$crawlA/${kind}?limit=500" $tenantAKey $null
        Assert-Status $response @(200) "$kind findings"
        $results[$kind] = @($response.Json.data)
    }
    if ($results.pages.Count -lt 2 -or $results.links.Count -lt 2 -or $results.resources.Count -lt 3 -or $results.issues.Count -lt 1) {
        throw "Fixture coverage is too small: issues=$($results.issues.Count), pages=$($results.pages.Count), links=$($results.links.Count), resources=$($results.resources.Count)"
    }

    $upstreamCrawl = Get-DatabaseScalar "SELECT upstream_crawl_id FROM api_crawls WHERE id='$crawlA';"
    $databaseCounts = @{
        pages = [int](Get-DatabaseScalar "SELECT COUNT(*) FROM pagereports WHERE crawl_id=$upstreamCrawl;")
        links = [int](Get-DatabaseScalar "SELECT (SELECT COUNT(*) FROM links WHERE crawl_id=$upstreamCrawl)+(SELECT COUNT(*) FROM external_links WHERE crawl_id=$upstreamCrawl);")
        resources = [int](Get-DatabaseScalar "SELECT (SELECT COUNT(*) FROM images WHERE crawl_id=$upstreamCrawl)+(SELECT COUNT(*) FROM scripts WHERE crawl_id=$upstreamCrawl)+(SELECT COUNT(*) FROM styles WHERE crawl_id=$upstreamCrawl)+(SELECT COUNT(*) FROM iframes WHERE crawl_id=$upstreamCrawl)+(SELECT COUNT(*) FROM audios WHERE crawl_id=$upstreamCrawl)+(SELECT COUNT(*) FROM videos WHERE crawl_id=$upstreamCrawl);")
        issues = [int](Get-DatabaseScalar "SELECT COUNT(*) FROM issues WHERE crawl_id=$upstreamCrawl;")
    }
    foreach ($kind in $databaseCounts.Keys) {
        if ($results[$kind].Count -ne $databaseCounts[$kind]) {
            throw "$kind API count $($results[$kind].Count) differs from upstream repository count $($databaseCounts[$kind])"
        }
    }

    foreach ($export in @("issues.csv", "pages.csv", "resources.csv", "sitemap.xml")) {
        $response = Invoke-SmokeRequest "GET" "/api/v1/projects/$projectA/crawls/$crawlA/exports/$export" $tenantAKey $null
        Assert-Status $response @(200) "$export export"
        if ([string]::IsNullOrWhiteSpace($response.Content)) { throw "$export export was empty" }
    }

    $tenantBList = Invoke-SmokeRequest "GET" "/api/v1/projects" $tenantBKey $null
    Assert-Status $tenantBList @(200) "tenant-b project list"
    if (@($tenantBList.Json.data).id -contains $projectA) { throw "tenant-b project list leaked tenant-a" }

    $foreignProject = Invoke-SmokeRequest "GET" "/api/v1/projects/$projectA" $tenantBKey $null
    $missingProject = Invoke-SmokeRequest "GET" "/api/v1/projects/00000000-0000-4000-8000-000000000099" $tenantBKey $null
    Assert-Status $foreignProject @(404) "foreign project probe"
    Assert-Status $missingProject @(404) "missing project probe"
    if ($foreignProject.Json.error.code -ne $missingProject.Json.error.code) { throw "Foreign and missing project responses are distinguishable" }

    $foreignRoutes = @(
        "/api/v1/projects/$projectB/crawls/$crawlA",
        "/api/v1/projects/$projectB/crawls/$crawlA/issues?limit=1",
        "/api/v1/projects/$projectB/crawls/$crawlA/pages?cursor=invalid",
        "/api/v1/projects/$projectB/crawls/$crawlA/exports/pages.csv"
    )
    foreach ($path in $foreignRoutes) {
        $probe = Invoke-SmokeRequest "GET" $path $tenantBKey $null
        Assert-Status $probe @(404) "foreign isolation probe $path"
    }
    $timingDelta = [math]::Abs($foreignProject.Milliseconds - $missingProject.Milliseconds)
    if ($timingDelta -gt 1000) { throw "Foreign/missing 404 timing delta was unexpectedly high: ${timingDelta}ms" }

    $meta = Invoke-SmokeRequest "GET" "/api/v1/meta" $platformKey $null
    Assert-Status $meta @(200) "metadata"
    $labels = (Invoke-Docker @("image", "inspect", $image, "--format", "{{json .Config.Labels}}") | Select-Object -Last 1) | ConvertFrom-Json
    if ($meta.Json.fork_revision -ne $forkRevision -or $labels."org.opencontainers.image.revision" -ne $forkRevision) { throw "Fork revision provenance mismatch" }
    if ($meta.Json.upstream_revision -ne $upstreamRevision -or $labels."io.kilnbench.seonaut.upstream.revision" -ne $upstreamRevision) { throw "Upstream revision provenance mismatch" }
    if ($meta.Json.schema_version -ne "80" -or $labels."io.kilnbench.seonaut.schema.version" -ne "80") { throw "Schema provenance mismatch" }

    if (-not $SkipRollback) {
        $projectCountBefore = [int](Get-DatabaseScalar "SELECT COUNT(*) FROM projects;")
        $crawlCountBefore = [int](Get-DatabaseScalar "SELECT COUNT(*) FROM crawls WHERE end IS NOT NULL;")
        if ($projectCountBefore -lt 2 -or $crawlCountBefore -lt 1) { throw "Rollback fixture has no durable project/crawl data" }
        Invoke-Docker ($compose + @("exec", "-T", "-e", "MYSQL_PWD=root-smoke", "db", "sh", "-c", "mysqldump -uroot --single-transaction seonaut > /tmp/pre-rollback.sql")) | Out-Null
        Invoke-Docker ($compose + @("stop", "app")) | Out-Null
        Invoke-Docker ($compose + @("--profile", "rollback", "up", "-d", "rollback")) | Out-Null
        Wait-ForUrl "$RollbackUrl/signin"
        $rollbackLabel = Invoke-Docker @("image", "inspect", "ghcr.io/stjudewashere/seonaut@$upstreamDigest", "--format", "{{index .Config.Labels `"org.opencontainers.image.revision`"}}")
        if (($rollbackLabel | Select-Object -Last 1).Trim() -ne $upstreamRevision) { throw "Pinned rollback image revision mismatch" }
        $projectCountAfter = [int](Get-DatabaseScalar "SELECT COUNT(*) FROM projects WHERE id IS NOT NULL AND url <> ''; ")
        $crawlCountAfter = [int](Get-DatabaseScalar "SELECT COUNT(*) FROM crawls WHERE end IS NOT NULL AND project_id IS NOT NULL;")
        if ($projectCountAfter -ne $projectCountBefore -or $crawlCountAfter -ne $crawlCountBefore) { throw "Pinned upstream rollback could not read the existing projects/crawls" }
    }

    Write-Host "PASS: provenance, fixture accuracy, two-tenant isolation, exports, and rollback compatibility"
    Write-Host "Counts: issues=$($results.issues.Count) pages=$($results.pages.Count) links=$($results.links.Count) resources=$($results.resources.Count)"
} finally {
    if (-not $KeepStack) {
        try { Invoke-Docker ($compose + @("down", "--volumes", "--remove-orphans")) | Out-Null } catch { Write-Warning $_ }
    }
    Pop-Location
}
