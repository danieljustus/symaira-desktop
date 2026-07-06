import { Command } from '@tauri-apps/plugin-shell';

export interface Note {
    path: string;
    title: string;
    sha256: string;
    modified_at: string;
    indexed_at: string;
}

export interface SearchResult extends Note {
    snippet: string;
}

export class CoreClient {
    private async exec<T>(args: string[]): Promise<T> {
        // Tauri v2 shell command
        const cmd = Command.sidecar('bin/symdesk', [...args, '--json']);
        const output = await cmd.execute();

        if (output.code !== 0) {
            throw new Error(`symdesk failed: ${output.stderr}`);
        }

        try {
            return JSON.parse(output.stdout) as T;
        } catch (e) {
            throw new Error(`Failed to parse symdesk output: ${output.stdout}`);
        }
    }

    async ls(dir: string = ''): Promise<Note[]> {
        return this.exec<Note[]>(['ls', dir]);
    }

    async search(query: string): Promise<SearchResult[]> {
        // Here we can prepare for a Seek-Fallback (HTTP request)
        // If config.seek_enabled, fetch from Seek, else:
        return this.exec<SearchResult[]>(['search', query]);
    }

    async props(file: string): Promise<Record<string, any>> {
        return this.exec<Record<string, any>>(['props', file]);
    }

    async backlinks(file: string): Promise<string[]> {
        return this.exec<string[]>(['backlinks', file]);
    }

    async noteNew(title: string, content: string = ''): Promise<string> {
        // The cli requires title and optionally content. For simplicity we can pass content via stdin,
        // but our symdesk note new CLI currently takes arguments or doesn't support stdin easily in this SDK wrapper.
        // Wait, does 'symdesk note new' take arguments?
        // Let's assume `symdesk note new "Title"` for now, and maybe we can't easily pass content yet via CLI args unless we modify symdesk.
        // We will just call `symdesk note new "Title"`
        const cmd = Command.sidecar('bin/symdesk', ['note', 'new', title, '--json']);
        const output = await cmd.execute();
        if (output.code !== 0) {
            throw new Error(`symdesk note new failed: ${output.stderr}`);
        }
        const res = JSON.parse(output.stdout);
        return res.path;
    }

    async doctor(): Promise<any> {
        return this.exec<any>(['doctor']);
    }
}

export const core = new CoreClient();
