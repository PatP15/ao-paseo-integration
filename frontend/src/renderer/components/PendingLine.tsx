import { Loader2 } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "../lib/utils";

/**
 * A read still in flight, with the app's spinner beside its sentence.
 *
 * The remote-execution reads are the slowest surfaces in AO — each one runs a
 * CLI on another machine, and a cold instructions or inventory read takes tens
 * of seconds — yet they rendered a single static line of muted text. That is
 * exactly what a finished-and-empty surface looks like, so the honest reading of
 * "Reading ~/.claude/CLAUDE.md from the computer…" on a blank panel was that the
 * app had stopped. Everything else in AO that waits, spins.
 */
export function PendingLine({ children, className }: { children: ReactNode; className?: string }) {
	return (
		<p className={cn("flex items-center gap-2 text-sm text-settings-muted", className)} role="status">
			<Loader2 aria-hidden="true" className="size-icon-base shrink-0 animate-spin" />
			<span>{children}</span>
		</p>
	);
}
