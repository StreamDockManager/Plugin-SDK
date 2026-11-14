import process from 'node:process';

/** Parent process ownership, not a claim that the host quit successfully.
 * A missing/reparented launch owner ends an idle plugin's lifetime. PID 1 and
 * detached launches are deliberately excluded; permission errors mean unknown.
 */
export function launchOwnerExited(
    owner: number,
    parent: number = process.ppid,
    probe: (pid: number) => void = (pid) => {
        process.kill(pid, 0);
    },
): boolean {
    if (!Number.isInteger(owner) || owner <= 1) return false;
    if (parent !== owner) return true;
    try {
        probe(owner);
        return false;
    } catch (error) {
        return (error as NodeJS.ErrnoException)?.code === 'ESRCH';
    }
}
