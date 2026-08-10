import { Loader2 } from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";
import { cn } from "../lib/utils";

/** How long a read may run before the spinner alone stops being an answer. */
const SLOW_AFTER_MS = 15_000;

/**
 * A read still in flight, with the app's spinner beside its sentence.
 *
 * The remote-execution reads are the slowest surfaces in AO — each one runs a
 * CLI on another machine, and a cold instructions or inventory read takes tens
 * of seconds — yet they rendered a single static line of muted text. That is
 * exactly what a finished-and-empty surface looks like, so the honest reading of
 * "Reading ~/.claude/CLAUDE.md from the computer…" on a blank panel was that the
 * app had stopped. Everything else in AO that waits, spins.
 *
 * A spinner answers "is it working?" but not "should I still be waiting?". A
 * remote read against a slow or silent computer holds this line for a minute or
 * more — measured at 93s on a rig whose maintenance channel had stalled — so
 * callers whose read leaves the machine pass `slowHint`, shown once the wait
 * passes {@link SLOW_AFTER_MS}. It says what the wait depends on; it never
 * claims the read has failed, because it has not.
 */
export function PendingLine({
	children,
	className,
	slowHint,
}: {
	children: ReactNode;
	className?: string;
	/** Shown after {@link SLOW_AFTER_MS}; omit for reads that stay local. */
	slowHint?: ReactNode;
}) {
	const [slow, setSlow] = useState(false);
	useEffect(() => {
		if (slowHint === undefined) return;
		const timer = setTimeout(() => setSlow(true), SLOW_AFTER_MS);
		return () => clearTimeout(timer);
	}, [slowHint]);

	return (
		<div className="flex flex-col gap-1">
			<p className={cn("flex items-center gap-2 text-sm text-settings-muted", className)} role="status">
				<Loader2 aria-hidden="true" className="size-icon-base shrink-0 animate-spin" />
				<span>{children}</span>
			</p>
			{slow && slowHint !== undefined ? (
				<p aria-live="polite" className={cn("pl-6 text-xs text-settings-muted", className)}>
					{slowHint}
				</p>
			) : null}
		</div>
	);
}
