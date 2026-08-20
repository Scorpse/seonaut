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

function Assert-Multiset {
    param(
        [Parameter(Mandatory)][object[]]$Actual,
        [Parameter(Mandatory)][object[]]$Expected,
        [Parameter(Mandatory)][string]$Name
    )
    $actualValues = @($Actual | ForEach-Object { [string]$_ } | Sort-Object)
    $expectedValues = @($Expected | ForEach-Object { [string]$_ } | Sort-Object)
    if (($actualValues -join "`n") -ne ($expectedValues -join "`n")) {
        throw "$Name differed from the controlled fixture.`nExpected:`n$($expectedValues -join "`n")`nActual:`n$($actualValues -join "`n")"
    }
}

function Get-OptionalField {
    param([Parameter(Mandatory)][object]$Object, [Parameter(Mandatory)][string]$Name)
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property -or $null -eq $property.Value) { return "" }
    return [string]$property.Value
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
        Invoke-Docker ($compose + @("--profile", "rollback", "down", "--volumes", "--remove-orphans")) | Out-Null
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
    $expectedCounts = @{ issues = 23; pages = 6; links = 4; resources = 3 }
    foreach ($kind in $expectedCounts.Keys) {
        if ($results[$kind].Count -ne $expectedCounts[$kind]) {
            throw "$kind fixture count was $($results[$kind].Count), expected $($expectedCounts[$kind])"
        }
    }

    $expectedIssues = @(
        "http://fixture/|ERROR_CONTENT_TYPE_OPTIONS|warning",
        "http://fixture/|ERROR_HTTP_LINKS|alert",
        "http://fixture/|ERROR_LITTLE_CONTENT|warning",
        "http://fixture/|ERROR_MISSING_CSP|warning",
        "http://fixture/|ERROR_MISSING_HSTS|warning",
        "http://fixture/|ERROR_SHORT_DESCRIPTION|alert",
        "http://fixture/|HTTP_SCHEME|critical",
        "http://fixture/about.html|ERROR_CONTENT_TYPE_OPTIONS|warning",
        "http://fixture/about.html|ERROR_MISSING_CSP|warning",
        "http://fixture/about.html|ERROR_MISSING_HSTS|warning",
        "http://fixture/app.js|ERROR_MISSING_HSTS|warning",
        "http://fixture/app.js|HTTP_SCHEME|critical",
        "http://fixture/missing.html|ERROR_40x|critical",
        "http://fixture/missing.html|ERROR_CONTENT_TYPE_OPTIONS|warning",
        "http://fixture/missing.html|ERROR_MISSING_CSP|warning",
        "http://fixture/missing.html|ERROR_MISSING_HSTS|warning",
        "http://fixture/missing.html|ERROR_MISSING_VIEWPORT|warning",
        "http://fixture/missing.html|ERROR_NO_LANG|warning",
        "http://fixture/missing.html|ERROR_SHORT_TITLE|alert",
        "http://fixture/pixel.svg|ERROR_MISSING_HSTS|warning",
        "http://fixture/pixel.svg|HTTP_SCHEME|critical",
        "http://fixture/site.css|ERROR_MISSING_HSTS|warning",
        "http://fixture/site.css|HTTP_SCHEME|critical"
    )
    Assert-Multiset @($results.issues | ForEach-Object { "$($_.page_url)|$($_.code)|$($_.severity)" }) $expectedIssues "JSON issue URL/code/severity multiset"

    $expectedPages = @(
        "http://fixture/|200|text/html|en|Kilnbench fixture home||true|true",
        "http://fixture/about.html|200|text/html|en||index,nofollow|false|true",
        "http://fixture/app.js|200|application/javascript||||true|false",
        "http://fixture/missing.html|404|text/html||404 Not Found||true|false",
        "http://fixture/pixel.svg|200|image/svg+xml||||true|false",
        "http://fixture/site.css|200|text/css||||true|false"
    )
    Assert-Multiset @($results.pages | ForEach-Object { "$($_.url)|$($_.status_code)|$(Get-OptionalField $_ 'content_type')|$(Get-OptionalField $_ 'language')|$(Get-OptionalField $_ 'title')|$(Get-OptionalField $_ 'robots')|$(([string]$_.crawled).ToLowerInvariant())|$(([string]$_.in_sitemap).ToLowerInvariant())" }) $expectedPages "JSON page multiset"
    $expectedLinks = @(
        "external|http://fixture/|https://example.com/|External reference|nofollow|true",
        "internal|http://fixture/|http://fixture/about.html|About||false",
        "internal|http://fixture/|http://fixture/missing.html|Missing page||false",
        "internal|http://fixture/about.html|http://fixture/|Home||false"
    )
    Assert-Multiset @($results.links | ForEach-Object { "$($_.kind)|$($_.origin_url)|$($_.destination_url)|$(Get-OptionalField $_ 'text')|$(Get-OptionalField $_ 'rel')|$(([string]$_.nofollow).ToLowerInvariant())" }) $expectedLinks "JSON link multiset"
    $expectedResources = @(
        "image|http://fixture/|http://fixture/pixel.svg|Fixture pixel|",
        "script|http://fixture/|http://fixture/app.js||",
        "style|http://fixture/|http://fixture/site.css||"
    )
    Assert-Multiset @($results.resources | ForEach-Object { "$($_.type)|$($_.origin_url)|$($_.url)|$(Get-OptionalField $_ 'alt')|$(Get-OptionalField $_ 'poster')" }) $expectedResources "JSON resource multiset"

    $upstreamCrawl = Get-DatabaseScalar "SELECT upstream_crawl_id FROM api_crawls WHERE id='$crawlA';"
    $upstreamProject = Get-DatabaseScalar "SELECT upstream_project_id FROM api_projects WHERE id='$projectA';"
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

    $exports = @{}
    foreach ($export in @("issues.csv", "pages.csv", "resources.csv", "sitemap.xml")) {
        $response = Invoke-SmokeRequest "GET" "/api/v1/projects/$projectA/crawls/$crawlA/exports/$export" $tenantAKey $null
        Assert-Status $response @(200) "$export export"
        if ([string]::IsNullOrWhiteSpace($response.Content)) { throw "$export export was empty" }
        $exports[$export] = $response.Content
    }

    $issueLabels = @{
        ERROR_40x = "URLs returning status code 40x"
        ERROR_CONTENT_TYPE_OPTIONS = "Missing content type options"
        ERROR_HTTP_LINKS = "HTTPS URLs with links to HTTP URLs"
        ERROR_LITTLE_CONTENT = "Pages with little content"
        ERROR_MISSING_CSP = "Missing content security policy"
        ERROR_MISSING_HSTS = "Missing HSTS header"
        ERROR_MISSING_VIEWPORT = "Pages missing the viewport meta tag"
        ERROR_NO_LANG = "Pages missing the language attribute"
        ERROR_SHORT_DESCRIPTION = "Pages with short meta description"
        ERROR_SHORT_TITLE = "Pages with short title"
        HTTP_SCHEME = "Pages using the HTTP scheme"
    }
    $priorityLabels = @{ critical = "Critical"; alert = "Alert"; warning = "Warning" }
    $expectedIssueCSV = @($expectedIssues | ForEach-Object {
        $parts = $_ -split '\|', 3
        "$($parts[0])|$($issueLabels[$parts[1]])|$($priorityLabels[$parts[2]])"
    })
    $issueCSVRows = @($exports["issues.csv"] | ConvertFrom-Csv)
    Assert-Multiset @($issueCSVRows | ForEach-Object { "$($_.URL)|$($_.'Issue Type')|$($_.Priority)" }) $expectedIssueCSV "issues.csv rows"

    $pageCSVRows = @($exports["pages.csv"] | ConvertFrom-Csv)
    $expectedPageCSV = @(
        "http://fixture/|200|text/html|en|Kilnbench fixture home|",
        "http://fixture/about.html|200|text/html|en||index,nofollow",
        "http://fixture/app.js|200|application/javascript|||",
        "http://fixture/missing.html|404|text/html||404 Not Found|",
        "http://fixture/pixel.svg|200|image/svg+xml|||",
        "http://fixture/site.css|200|text/css|||"
    )
    Assert-Multiset @($pageCSVRows | ForEach-Object { "$($_.URL)|$($_.'Status Code')|$($_.'Content Type')|$($_.Lang)|$($_.Title)|$($_.Robots)" }) $expectedPageCSV "pages.csv stable columns"

    $resourceCSVRows = @($exports["resources.csv"] | ConvertFrom-Csv)
    Assert-Multiset @($resourceCSVRows | ForEach-Object { "$($_.Type)|$($_.Origin)|$($_.URL)|$($_.Alt)|$($_.Poster)" }) $expectedResources "resources.csv rows"
    $sitemapLocations = @([regex]::Matches($exports["sitemap.xml"], '<loc>([^<]+)</loc>') | ForEach-Object { $_.Groups[1].Value })
    Assert-Multiset $sitemapLocations @("http://fixture/") "sitemap.xml locations"

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
    $tenantACursorResponse = Invoke-SmokeRequest "GET" "/api/v1/projects/$projectA/crawls/$crawlA/issues?limit=1" $tenantAKey $null
    Assert-Status $tenantACursorResponse @(200) "tenant-a cursor source"
    $tenantACursor = [string]$tenantACursorResponse.Json.page.next_cursor
    if ([string]::IsNullOrWhiteSpace($tenantACursor)) { throw "tenant-a cursor source returned no next cursor" }
    $cursorTransplant = Invoke-SmokeRequest "GET" "/api/v1/projects/$projectB/crawls/$crawlA/issues?cursor=$tenantACursor" $tenantBKey $null
    Assert-Status $cursorTransplant @(404) "foreign valid-cursor transplant"
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

        $rollbackFixtureEmail = "rollback-check@example.invalid"
        $rollbackPassword = "rollback-smoke-password"
        $signup = Invoke-WebRequest -Method POST -Uri "$BaseUrl/signup" -Body @{ email = $rollbackFixtureEmail; password = $rollbackPassword } -WebSession (New-Object Microsoft.PowerShell.Commands.WebRequestSession) -SkipHttpErrorCheck
        if ([int]$signup.StatusCode -ne 200) { throw "Could not create the disposable rollback login fixture" }
        $rollbackEmail = Get-DatabaseScalar "SELECT service_email FROM api_tenants WHERE external_tenant_id='tenant-a' LIMIT 1;"
        $rollbackUserPrepared = [int](Get-DatabaseScalar "UPDATE users service_user JOIN users fixture_user ON fixture_user.email='$rollbackFixtureEmail' SET service_user.password=fixture_user.password, service_user.api_only=0 WHERE service_user.email='$rollbackEmail'; SELECT ROW_COUNT();")
        if ($rollbackUserPrepared -ne 1) { throw "Could not prepare the tenant-a user for upstream UI compatibility verification" }
        Get-DatabaseScalar "DELETE FROM users WHERE email='$rollbackFixtureEmail'; SELECT ROW_COUNT();" | Out-Null

        Invoke-Docker ($compose + @("stop", "app")) | Out-Null
        Invoke-Docker ($compose + @("--profile", "rollback", "up", "-d", "rollback")) | Out-Null
        Wait-ForUrl "$RollbackUrl/signin"
        $rollbackLabel = Invoke-Docker @("image", "inspect", "ghcr.io/stjudewashere/seonaut@$upstreamDigest", "--format", "{{index .Config.Labels `"org.opencontainers.image.revision`"}}")
        if (($rollbackLabel | Select-Object -Last 1).Trim() -ne $upstreamRevision) { throw "Pinned rollback image revision mismatch" }
        $projectCountAfter = [int](Get-DatabaseScalar "SELECT COUNT(*) FROM projects WHERE id IS NOT NULL AND url <> ''; ")
        $crawlCountAfter = [int](Get-DatabaseScalar "SELECT COUNT(*) FROM crawls WHERE end IS NOT NULL AND project_id IS NOT NULL;")
        if ($projectCountAfter -ne $projectCountBefore -or $crawlCountAfter -ne $crawlCountBefore) { throw "Pinned upstream rollback could not read the existing projects/crawls" }
        $rollbackSession = New-Object Microsoft.PowerShell.Commands.WebRequestSession
        $login = Invoke-WebRequest -Method POST -Uri "$RollbackUrl/signin" -Body @{ email = $rollbackEmail; password = $rollbackPassword } -WebSession $rollbackSession -SkipHttpErrorCheck
        if ([int]$login.StatusCode -ne 200) { throw "Pinned upstream rollback login failed" }
        $rollbackHome = Invoke-WebRequest -Uri "$RollbackUrl/" -WebSession $rollbackSession -SkipHttpErrorCheck
        if ([int]$rollbackHome.StatusCode -ne 200 -or $rollbackHome.Content -notmatch [regex]::Escape("http://fixture/") -or $rollbackHome.Content -notmatch [regex]::Escape("/dashboard?pid=$upstreamProject")) {
            throw "Pinned upstream UI did not render the retained project and completed crawl"
        }
        $rollbackDashboard = Invoke-WebRequest -Uri "$RollbackUrl/dashboard?pid=$upstreamProject" -WebSession $rollbackSession -SkipHttpErrorCheck
        if ([int]$rollbackDashboard.StatusCode -ne 200 -or $rollbackDashboard.BaseResponse.RequestMessage.RequestUri.AbsolutePath -ne "/dashboard" -or $rollbackDashboard.Content -notmatch [regex]::Escape("6 URLs crawled")) {
            throw "Pinned upstream dashboard could not read the retained crawl"
        }
    }

    Write-Host "PASS: provenance, fixture accuracy, two-tenant isolation, exports, and rollback compatibility"
    Write-Host "Counts: issues=$($results.issues.Count) pages=$($results.pages.Count) links=$($results.links.Count) resources=$($results.resources.Count)"
} finally {
    if (-not $KeepStack) {
        try { Invoke-Docker ($compose + @("--profile", "rollback", "down", "--volumes", "--remove-orphans")) | Out-Null } catch { Write-Warning $_ }
    }
    Pop-Location
}
