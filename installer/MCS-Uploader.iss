; The installer for MCS Uploader. It is made with Inno Setup 6: release.cmd
; builds the program and then this, into dist\MCS-Uploader-Setup-v<version>.exe.
;
; It installs for the person running it only, so it never asks for an
; administrator, and it puts the program in the Start menu and (unless
; unticked) on the desktop. Before installing it asks the program itself what
; this computer hasn't got that the program uses (MCS-Uploader.exe -check),
; and offers to install the one thing it can: ffmpeg, through winget.

#define AppName "MCS Uploader"
#define AppExe "MCS-Uploader.exe"

; The version is the one in the VERSION file, which the program carries too.
#define VersionFile FileOpen(AddBackslash(SourcePath) + "..\VERSION")
#define AppVersion Trim(FileRead(VersionFile))
#expr FileClose(VersionFile)
#undef VersionFile

; What winget calls the ffmpeg it installs: the smaller of the two builds it
; has, which has everything the videos need.
#define FFmpegPackage "Gyan.FFmpeg.Essentials"

[Setup]
; AppId tells Windows that a later version is the same program, to be
; installed over this one. Never change it.
AppId={{DC967CB9-0E77-4BF4-8AE5-BFCF9A7A2D3F}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} v{#AppVersion}
AppPublisher=Micro Club Sprint
AppPublisherURL=https://mcs.fpv-events.com
AppSupportURL=https://mcs.fpv-events.com
VersionInfoVersion={#AppVersion}
VersionInfoProductName={#AppName}
VersionInfoDescription={#AppName} Setup
PrivilegesRequired=lowest
DefaultDirName={autopf}\{#AppName}
DisableProgramGroupPage=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
; The program holds this while it runs (runningMutex in version.go), so Setup
; asks for it to be closed before replacing or removing it.
AppMutex=MCS-Uploader-Running
SetupIconFile=..\icon.ico
WizardSmallImageFile=wizard.bmp
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
OutputDir=..\dist
OutputBaseFilename=MCS-Uploader-Setup-v{#AppVersion}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern

[Tasks]
Name: "desktopicon"; Description: "Put a shortcut on the &desktop"; GroupDescription: "Shortcuts:"

[Files]
Source: "..\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\{#AppName}"; Filename: "{app}\{#AppExe}"; Comment: "Upload FPVTrackside times and videos to Micro Club Sprint"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Comment: "Upload FPVTrackside times and videos to Micro Club Sprint"; Tasks: desktopicon

[Run]
Filename: "{app}\{#AppExe}"; Description: "Start {#AppName}"; Flags: nowait postinstall skipifsilent

[Code]
const
  // What the program's -check answers with: the sum of whichever apply
  // (missingFFmpeg and missingTrackside in version.go).
  MissingFFmpeg = 1;
  MissingTrackside = 2;

var
  Missing: Integer;        // the program's answer for this computer
  FFmpegBox: TNewCheckBox; // "install ffmpeg now"; nil when not on offer

// Winget is where Windows' own installer of programs is, or '' if this
// computer hasn't got it.
function Winget(): String;
begin
  Result := ExpandConstant('{localappdata}\Microsoft\WindowsApps\winget.exe');
  if not FileExists(Result) then
    Result := FileSearch('winget.exe', GetEnv('PATH'));
end;

// WhatIsMissing asks a copy of the program what this computer hasn't got.
// If the program can't be asked, nothing is said to be missing.
function WhatIsMissing(const Exe: String): Integer;
var
  Code: Integer;
begin
  Result := 0;
  if Exec(Exe, '-check', '', SW_HIDE, ewWaitUntilTerminated, Code) then
    if (Code >= 0) and (Code <= MissingFFmpeg + MissingTrackside) then
      Result := Code;
end;

function Lacks(const What: Integer): Boolean;
begin
  Result := (Missing and What) <> 0;
end;

// AddText puts a paragraph on a page under the last one, and returns where
// the next goes.
function AddText(const Page: TWizardPage; const Top: Integer; const Text: String; const Bold: Boolean): Integer;
var
  T: TNewStaticText;
begin
  T := TNewStaticText.Create(Page);
  T.Parent := Page.Surface;
  T.Left := 0;
  T.Top := Top;
  T.Width := Page.SurfaceWidth;
  T.AutoSize := False;
  T.WordWrap := True;
  if Bold then
    T.Font.Style := [fsBold];
  T.Caption := Text;
  T.AdjustHeight();
  Result := T.Top + T.Height;
end;

// The page saying what was looked for and what was found.
procedure CreateNeedsPage();
var
  Page: TWizardPage;
  Y: Integer;
begin
  Page := CreateCustomPage(wpSelectTasks, 'What {#AppName} needs',
    'Setup has looked on this computer for the programs {#AppName} works with.');
  Y := 0;

  if Lacks(MissingTrackside) then
  begin
    Y := AddText(Page, Y, 'FPVTrackside: not found', True) + ScaleY(4);
    Y := AddText(Page, Y, '{#AppName} reads the events FPVTrackside saves, so it has nothing to show ' +
      'until FPVTrackside is installed on this computer and has run an event. ' +
      'You can carry on and install {#AppName} first.', False);
  end
  else
  begin
    Y := AddText(Page, Y, 'FPVTrackside: found', True) + ScaleY(4);
    Y := AddText(Page, Y, '{#AppName} reads the events it saves.', False);
  end;
  Y := Y + ScaleY(16);

  if not Lacks(MissingFFmpeg) then
  begin
    Y := AddText(Page, Y, 'ffmpeg: found', True) + ScaleY(4);
    Y := AddText(Page, Y, 'Export Video uses it to cut each pilot''s video out of FPVTrackside''s recordings.', False);
  end
  else if Winget() <> '' then
  begin
    Y := AddText(Page, Y, 'ffmpeg: not found', True) + ScaleY(4);
    Y := AddText(Page, Y, 'Export Video uses it to cut each pilot''s video out of FPVTrackside''s recordings. ' +
      'Everything else works without it.', False) + ScaleY(8);
    FFmpegBox := TNewCheckBox.Create(Page);
    FFmpegBox.Parent := Page.Surface;
    FFmpegBox.Left := 0;
    FFmpegBox.Top := Y;
    FFmpegBox.Width := Page.SurfaceWidth;
    FFmpegBox.Height := ScaleY(17);
    FFmpegBox.Caption := '&Download and install ffmpeg as well (about 110 MB, with Windows'' own winget)';
    FFmpegBox.Checked := True;
  end
  else
  begin
    Y := AddText(Page, Y, 'ffmpeg: not found', True) + ScaleY(4);
    Y := AddText(Page, Y, 'Export Video uses it to cut each pilot''s video out of FPVTrackside''s recordings, ' +
      'and won''t work until ffmpeg is installed. Everything else works without it. ' +
      'Setup can''t install it on this computer, as Windows'' winget isn''t here: ' +
      'see https://ffmpeg.org/download.html, and the "Export video" part of {#AppName}''s README.', False);
  end;
end;

procedure InitializeWizard();
begin
  // Ask the program itself, so Setup and the program can't disagree about
  // where ffmpeg might be.
  ExtractTemporaryFile('{#AppExe}');
  Missing := WhatIsMissing(ExpandConstant('{tmp}\{#AppExe}'));
  CreateNeedsPage();
end;

// InstallFFmpeg has winget fetch ffmpeg, then asks the installed program
// whether it can find it now.
procedure InstallFFmpeg();
var
  Code: Integer;
  Ran: Boolean;
begin
  WizardForm.StatusLabel.Caption := 'Downloading and installing ffmpeg. This can take a few minutes...';
  WizardForm.FilenameLabel.Caption := '';
  WizardForm.ProgressGauge.Style := npbstMarquee;
  try
    Ran := Exec(Winget(), 'install --id {#FFmpegPackage} --exact --source winget ' +
      '--accept-source-agreements --accept-package-agreements --disable-interactivity',
      '', SW_HIDE, ewWaitUntilTerminated, Code);
  finally
    WizardForm.ProgressGauge.Style := npbstNormal;
  end;
  if (WhatIsMissing(ExpandConstant('{app}\{#AppExe}')) and MissingFFmpeg) <> 0 then
  begin
    if not Ran then
      Code := -1;
    SuppressibleMsgBox('ffmpeg couldn''t be installed (winget answered ' + IntToStr(Code) + ').' + #13#10#13#10 +
      '{#AppName} is installed and works; only Export Video needs ffmpeg. To install it yourself, ' +
      'open a terminal and run:' + #13#10#13#10 + '    winget install {#FFmpegPackage}',
      mbInformation, MB_OK, IDOK);
  end;
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if (CurStep = ssPostInstall) and (FFmpegBox <> nil) and FFmpegBox.Checked then
    InstallFFmpeg();
end;

// Removing the program leaves the settings, which hold the API key, unless
// the person says to remove them too.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Folder: String;
begin
  if (CurUninstallStep <> usPostUninstall) or UninstallSilent() then
    exit;
  Folder := ExpandConstant('{userappdata}\{#AppName}');
  if DirExists(Folder) then
    if MsgBox('Remove your {#AppName} settings as well?' + #13#10#13#10 +
      'They hold your MCS API key, your track, and which events you have uploaded. ' +
      'Choose No if you are going to install {#AppName} again.',
      mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
      DelTree(Folder, True, True, True);
end;
