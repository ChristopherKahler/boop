' Runs start-boop.ps1 with no window. The "Boop alert hub" scheduled task
' calls this at sign-in; double-clicking it starts the hub by hand.
Set fso = CreateObject("Scripting.FileSystemObject")
here = fso.GetParentFolderName(WScript.ScriptFullName)
CreateObject("WScript.Shell").Run "powershell.exe -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File """ & here & "\start-boop.ps1""", 0, False
