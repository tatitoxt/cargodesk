import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "CargoDesk",
  description: "Logistics operations desk demo with synthetic data",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>
        <div className="demo-banner" role="note">
          Demo environment — all organizations, people, accounts and places are synthetic.
        </div>
        <main>{children}</main>
      </body>
    </html>
  );
}
