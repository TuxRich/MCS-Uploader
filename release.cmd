@echo off
rem Builds MCS-Uploader.exe and its installer, dist\MCS-Uploader-Setup-v<version>.exe.
rem The version is the one in the VERSION file. Needs Go, and Inno Setup 6:
rem     winget install JRSoftware.InnoSetup
setlocal
cd /d "%~dp0"
set /p VERSION=<VERSION

set "ISCC="
for %%I in (ISCC.exe) do set "ISCC=%%~$PATH:I"
if not defined ISCC if exist "%LOCALAPPDATA%\Programs\Inno Setup 6\ISCC.exe" set "ISCC=%LOCALAPPDATA%\Programs\Inno Setup 6\ISCC.exe"
if not defined ISCC if exist "%ProgramFiles(x86)%\Inno Setup 6\ISCC.exe" set "ISCC=%ProgramFiles(x86)%\Inno Setup 6\ISCC.exe"
if not defined ISCC if exist "%ProgramFiles%\Inno Setup 6\ISCC.exe" set "ISCC=%ProgramFiles%\Inno Setup 6\ISCC.exe"
if not defined ISCC goto noinno

echo Testing...
go vet ./... || exit /b 1
go test ./... || exit /b 1
echo Building MCS-Uploader.exe v%VERSION%...
go build -ldflags="-H windowsgui" -o MCS-Uploader.exe || exit /b 1
echo Building the installer...
"%ISCC%" /Q installer\MCS-Uploader.iss || exit /b 1
echo.
echo Made dist\MCS-Uploader-Setup-v%VERSION%.exe
exit /b 0

:noinno
echo Inno Setup 6 wasn't found. Install it with:
echo     winget install JRSoftware.InnoSetup
exit /b 1
