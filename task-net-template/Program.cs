using System.Reflection;
using System.Diagnostics;
using System.Runtime.InteropServices;


string binaryName = "task.exe";

string packageRoot = Path.GetDirectoryName(Path.GetDirectoryName(Path.GetDirectoryName(Path.GetDirectoryName(AppContext.BaseDirectory))));
string exePath = Path.Combine(packageRoot, "runtimes", RuntimeInformation.RuntimeIdentifier, "native", binaryName);

var process = Process.Start(exePath, string.Join(" ", args));
process.WaitForExit();
return process.ExitCode;
