import type { Metadata } from 'next';
import { Nav } from '@/components/nav';
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
        <header className="topbar">
          <div className="topbar-inner">
            <a className="brand" href="/">
              curator<span>one binary</span>
            </a>
            <Nav />
          </div>
        </header>
        <main className="shell">{children}</main>
      </body>
    </html>
  );
}
