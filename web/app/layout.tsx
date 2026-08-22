import type { Metadata } from 'next';
import { Topbar } from '@/components/topbar';
import { Gate } from '@/components/gate';
import './globals.css';

export const metadata: Metadata = {
  title: 'curator',
  description: 'One binary where the *arr stack used to be.',
};

// The shell every screen sits in. It is a server component only in the sense
// that it does no I/O — there is no server at runtime, just a static export
// inside the Go binary.
export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        {/* A client component so this layout is not one: it owns the
            scrolled/at-rest state and nothing else moved. */}
        <Topbar />
        {/* Gate stands between the shell and the page, not in front of the
            whole document: the nav stays drawn while curator is locked, so the
            URL a deep link asked for is still the URL in the bar when the
            password is accepted. It renders the login form INSTEAD of children
            rather than navigating anywhere — see components/gate.tsx. */}
        <main className="shell">
          <Gate>{children}</Gate>
        </main>
      </body>
    </html>
  );
}
